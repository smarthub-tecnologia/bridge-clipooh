# mobio-bridge — Referência de API para o Dashboard UI

**Destinatário:** Equipe de desenvolvimento do dashboard administrativo  
**Versão:** v0.4.0  
**Base URL:** `https://bridge.mobiochat.com`  
**Última atualização:** 2026-05-04

---

## Índice

1. [Visão Geral da Arquitetura](#1-visão-geral-da-arquitetura)
2. [Autenticação](#2-autenticação)
3. [Detalhes de um Tenant](#3-detalhes-de-um-tenant)
4. [Formulário de Provisionamento](#4-formulário-de-provisionamento)
5. [Health Checks](#5-health-checks)
6. [Métricas Operacionais](#6-métricas-operacionais)
7. [Gestão Centralizada por Tenant](#7-gestão-centralizada-por-tenant)
8. [Lifecycle — Estratégia de Desativação](#8-lifecycle--estratégia-de-desativação)
9. [Exemplos Completos de Request/Response](#9-exemplos-completos-de-requestresponse)
10. [Glossário](#10-glossário)

---

## 1. Visão Geral da Arquitetura

O **mobio-bridge** é um serviço Go que atua como hub de tradução bidirecional entre dois sistemas externos:

- **Evolution GO** — gateway WhatsApp (envia e recebe mensagens via protocolo WhatsApp Web)
- **Chatwoot** — CRM/helpdesk onde os agentes humanos respondem clientes

O bridge recebe webhooks de ambos os lados, traduz os payloads e faz o repasse. Cada cliente da plataforma é um **tenant** com conta Chatwoot e instância Evolution GO provisionadas de forma isolada.

### Diagrama do fluxo completo

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Dashboard UI                                │
│          (lê /admin/metrics, cria tenants, gerencia planos)         │
└──────────────────────────┬──────────────────────────────────────────┘
                           │ HTTPS  Authorization: Bearer <ADMIN_API_KEY>
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│                 mobio-bridge  (bridge.mobiochat.com)                │
│                                                                     │
│  ┌──────────────┐   ┌───────────────┐   ┌─────────────────────┐   │
│  │ Admin API    │   │ Evolution     │   │ Chatwoot            │   │
│  │ /api/v1/     │   │ Webhook       │   │ Webhook             │   │
│  │ admin/*      │   │ /webhook/     │   │ /webhook/chatwoot   │   │
│  │              │   │ evolution     │   │ ?tenant={id}        │   │
│  └──────┬───────┘   └──────┬────────┘   └──────────┬──────────┘   │
│         │                  │                        │              │
│         │          ┌───────▼────────────────────────▼──────┐      │
│         │          │          BridgeService                 │      │
│         │          │  - dedup por message_id (Redis 24h)   │      │
│         │          │  - FindOrCreateContact                 │      │
│         │          │  - FindOrCreateConversation            │      │
│         │          │  - roteamento texto/mídia/áudio        │      │
│         │          └───────────────────────────────────────┘      │
│         │                                                          │
│  ┌──────▼──────────────────────────────────────────────────────┐  │
│  │              AdminService (provisioning)                     │  │
│  │   Phase 1: chamadas externas (Chatwoot + Evolution API)     │  │
│  │   Phase 2: commit atômico no PostgreSQL                     │  │
│  └─────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌────────────┐  ┌────────────┐  ┌───────────┐  ┌─────────────┐  │
│  │ PostgreSQL │  │   Redis    │  │   Queue   │  │Circuit Break│  │
│  │ (tenants,  │  │ (cache,    │  │ (retry,   │  │(Evolution   │  │
│  │  usage,    │  │  dedup,    │  │  jobs)    │  │ GO 30s)     │  │
│  │  inboxes)  │  │  sessions) │  │           │  │             │  │
│  └────────────┘  └────────────┘  └───────────┘  └─────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
         │                                         │
         ▼                                         ▼
┌─────────────────┐                    ┌───────────────────────┐
│  Evolution GO   │                    │      Chatwoot         │
│  (WhatsApp GW)  │◄──────────────────►│   (CRM / Helpdesk)   │
│                 │  mensagens         │                       │
└─────────────────┘                    └───────────────────────┘
         ▲
         │ WhatsApp Web Protocol
         ▼
┌─────────────────┐
│    Usuário      │
│   WhatsApp      │
└─────────────────┘
```

### Fluxo A — Inbound (WhatsApp → Chatwoot)

1. Usuário envia mensagem no WhatsApp
2. Evolution GO dispara webhook `POST /webhook/evolution?instance={nome}&tenant={id}`
3. Bridge faz deduplicação por `message_id` (Redis, TTL 24h)
4. Bridge chama `FindOrCreateContact` no Chatwoot (busca por telefone, cria se não existe)
5. Bridge chama `FindOrCreateConversation` (reutiliza conversa open/pending ou cria nova)
6. Bridge envia a mensagem/mídia para a conversa no Chatwoot

### Fluxo B — Outbound (Chatwoot → WhatsApp)

1. Agente responde no Chatwoot
2. Chatwoot dispara webhook `POST /webhook/chatwoot?tenant={id}`
3. Bridge valida assinatura HMAC-SHA256 com secret do tenant
4. Bridge extrai telefone do destinatário de `conversation.meta.sender.phone_number`
5. Bridge chama Evolution GO `/send/text` ou `/send/media` com token da instância

---

## 2. Autenticação

Todas as rotas `/api/v1/admin/*` exigem o header:

```
Authorization: Bearer <ADMIN_API_KEY>
```

O valor de `ADMIN_API_KEY` é definido como variável de ambiente no servidor. Sem esse header, ou com token incorreto, a resposta é `401 Unauthorized`.

**Rotas públicas** (sem autenticação):
- `GET /health/live`
- `GET /health/ready`
- `POST /webhook/evolution`
- `POST /webhook/chatwoot`

**Rotas protegidas por `BRIDGE_API_KEY`** (usado pelo sistema de notificações):
- `POST /api/v1/notify/send`
- `POST /api/v1/notify/template`
- `GET /api/v1/instances`

---

## 3. Detalhes de um Tenant

### `GET /api/v1/admin/tenants/{id}`

Retorna todos os dados de configuração de um tenant.

**Parâmetro de rota:** `id` — UUID do tenant

**Response 200:**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "Empresa Alpha",
  "domain": "alpha.com",
  "external_id": "erp-client-0042",
  "status": "active",
  "plan_id": 2,
  "chatwoot_account_id": 17,
  "chatwoot_account_name": null,
  "chatwoot_api_token": "xBZ9...(omitido na UI)",
  "evolution_base_url": "https://evo.mobiochat.com",
  "evolution_default_instance": "alpha-a3f2e1",
  "webhook_evolution_configured": true,
  "webhook_chatwoot_configured": true,
  "chatwoot_webhook_secret": "abc123...(omitido na UI)",
  "metadata": null,
  "created_at": "2025-11-01T14:30:00Z",
  "updated_at": "2026-01-15T09:12:00Z"
}
```

**Response 404:**

```json
{
  "error": "Tenant not found",
  "code": "TENANT_NOT_FOUND",
  "details": null
}
```

### Referência de campos

| Campo | Tipo | Descrição | Exibir na UI |
|---|---|---|---|
| `id` | `string (UUID)` | Identificador único do tenant no bridge | Sim — copiar como ID primário |
| `name` | `string` | Nome da empresa / workspace | Sim |
| `domain` | `string\|null` | Domínio customizado (opcional) | Sim |
| `external_id` | `string\|null` | ID do tenant no sistema externo (ERP, plataforma SaaS) | Sim — útil para debug |
| `status` | `string` | Estado: `"active"` ou `"inactive"` | Sim — badge colorido |
| `plan_id` | `int\|null` | ID do plano contratado (1 = Free, 2+ = pagos) | Sim — exibir nome do plano |
| `chatwoot_account_id` | `int\|null` | ID da conta no Chatwoot | Sim — link direto para o Chatwoot |
| `chatwoot_account_name` | `string\|null` | Nome da conta no Chatwoot (pode ser null) | Opcional |
| `chatwoot_api_token` | `string\|null` | Token de API da conta Chatwoot | **NÃO exibir** — campo sensível |
| `evolution_base_url` | `string\|null` | URL base da instância Evolution GO | Não (interno) |
| `evolution_default_instance` | `string\|null` | Nome da instância WhatsApp ativa | Sim |
| `webhook_evolution_configured` | `bool` | Webhook da Evolution GO configurado? | Sim — badge verde/vermelho |
| `webhook_chatwoot_configured` | `bool` | Webhook do Chatwoot configurado? | Sim — badge verde/vermelho |
| `chatwoot_webhook_secret` | `string\|null` | Secret HMAC do webhook Chatwoot | **NÃO exibir** — campo sensível |
| `metadata` | `object\|null` | Dados arbitrários extras | Opcional (JSON raw) |
| `created_at` | `ISO 8601` | Data de criação do tenant | Sim |
| `updated_at` | `ISO 8601` | Última modificação | Sim |

**Campos sensíveis que NÃO devem aparecer na UI:**
- `chatwoot_api_token` — token de API que autentica na conta Chatwoot do cliente
- `chatwoot_webhook_secret` — segredo HMAC usado para validar webhooks; expô-lo permite falsificar eventos

**Como interpretar `webhook_*_configured`:**

| Combinação | Significado | Badge |
|---|---|---|
| ambos `true` | Tenant totalmente operacional | 🟢 Ativo |
| `webhook_evolution_configured: false` | Webhook WhatsApp perdido — mensagens inbound não chegam | 🔴 Reconnect necessário |
| `webhook_chatwoot_configured: false` | Webhook Chatwoot não configurado — respostas de agentes não chegam | 🔴 Secret inválido |
| ambos `false` | Provisionamento incompleto | 🔴 Reprovisionar |

---

## 4. Formulário de Provisionamento

### `POST /api/v1/admin/tenants`

Provisiona um novo tenant de forma atômica: cria conta Chatwoot, usuário administrador, inbox, instância Evolution GO e configura webhooks. Todos os registros de banco são gravados numa única transação.

**Headers:**
```
Authorization: Bearer <ADMIN_API_KEY>
Content-Type: application/json
```

### Request

```json
{
  "workspace_id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "Empresa Alpha",
  "domain": "alpha.com",
  "external_id": "erp-client-0042",
  "plan_id": 1,
  "admin_email": "admin@alpha.com",
  "admin_password": "senha-segura-123",
  "instance_name": "alpha-whatsapp"
}
```

### Campos do request

| Campo | Tipo | Obrigatório | Validação | Descrição |
|---|---|---|---|---|
| `workspace_id` | `string (UUID)` | Não | UUID v4 | ID desejado para o tenant. Se omitido, o bridge gera automaticamente. Use o mesmo ID do seu sistema para facilitar lookup posterior. |
| `name` | `string` | **Sim** | não vazio | Nome da empresa. Usado como nome da conta no Chatwoot. |
| `domain` | `string` | Não | — | Domínio customizado da empresa. Só informativo. |
| `external_id` | `string` | Não | — | ID do cliente no sistema externo (ERP, CRM de origem). Facilita correlação. |
| `plan_id` | `int` | Não | inteiro positivo | ID do plano. Default: `1` (Free). |
| `admin_email` | `string` | **Sim** | email válido | Email do usuário administrador criado no Chatwoot. |
| `admin_password` | `string` | **Sim** | não vazio | Senha do usuário administrador no Chatwoot. Mínimo 8 caracteres recomendado. |
| `instance_name` | `string` | Não | — | Nome base para a instância WhatsApp. Será sufixado com um hash curto (ex: `alpha-whatsapp-a3f2e1`). Se omitido, gera `inst-{hash}`. |

### Response 201 — Sucesso

```json
{
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "chatwoot_account_id": 17,
  "chatwoot_inbox_id": 5,
  "evolution_instance_id": "alpha-whatsapp-a3f2e1",
  "qr_code": "",
  "status": "active",
  "correlation_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

### Campos do response

| Campo | Tipo | Descrição |
|---|---|---|
| `tenant_id` | `string (UUID)` | ID do tenant criado (guardar como chave primária) |
| `chatwoot_account_id` | `int` | ID da conta no Chatwoot (link direto: `https://mobiochat.com/app/accounts/{id}`) |
| `chatwoot_inbox_id` | `int` | ID da inbox WhatsApp criada no Chatwoot |
| `evolution_instance_id` | `string` | Nome da instância Evolution GO (usado nas chamadas de conexão) |
| `qr_code` | `string` | Sempre vazio nesta versão — QR code disponível via endpoint separado da Evolution GO |
| `status` | `string` | `"active"` em sucesso; pode retornar o status atual se tenant já existia |
| `correlation_id` | `string` | Espelho de `tenant_id` — útil para rastrear a requisição em logs |

### Erros possíveis

| HTTP | `code` | Quando ocorre | Como exibir |
|---|---|---|---|
| `400` | `MISSING_FIELDS` | `name`, `admin_email` ou `admin_password` ausentes | Destacar campos obrigatórios no formulário |
| `400` | `INVALID_PAYLOAD` | JSON malformado | "Dados inválidos, verifique o formulário" |
| `500` | `PROVISIONING_FAILED` | Falha em qualquer etapa externa (Chatwoot API, Evolution GO) | Exibir mensagem de erro do campo `error` + sugerir retry |

**Formato de erro:**
```json
{
  "error": "failed to create chatwoot account: chatwoot api error: status 500",
  "code": "PROVISIONING_FAILED",
  "details": null
}
```

### Idempotência — o que acontece ao enviar `workspace_id` duplicado

O bridge implementa idempotência por `workspace_id`:

1. Ao receber o `workspace_id`, busca o tenant no banco
2. Se encontrar, verifica se os serviços externos (Chatwoot + Evolution) ainda existem
3. Se **tudo está OK** (conta existe, instância ativa, webhooks configurados): retorna o mesmo response sem criar nada novo
4. Se **parcialmente quebrado** (ex: instância Evolution deletada): recria apenas o que faltou e atualiza o banco
5. Se **não encontrar**: provisiona normalmente como novo tenant

**Implicação para a UI:** é seguro enviar a mesma requisição duas vezes com o mesmo `workspace_id`. O response será idêntico. Use isso para implementar retry automático em caso de timeout.

---

## 5. Health Checks

### `GET /health/live`

**Propósito:** Confirma que o processo está vivo. Não verifica dependências.

**Autenticação:** Nenhuma (rota pública)

**Response — 200 OK (sempre):**
```json
{"status": "live"}
```

**Uso no dashboard:** Polling a cada 30s. Se deixar de responder, o container foi reiniciado ou crashou.

---

### `GET /health/ready`

**Propósito:** Verifica se todas as dependências estão operacionais antes de aceitar tráfego.

**Autenticação:** Nenhuma (rota pública)

**Response — 200 OK (tudo saudável):**
```json
{
  "status": "ready",
  "dependencies": {
    "postgres": "ok",
    "redis": "ok",
    "evolution": "ok"
  }
}
```

**Response — 503 Service Unavailable (alguma dependência com falha):**
```json
{
  "status": "degraded",
  "dependencies": {
    "postgres": "ok",
    "redis": "error",
    "evolution": "ok"
  }
}
```

### Valores possíveis por dependência

| Valor | Significado |
|---|---|
| `"ok"` | Respondeu dentro de 3 segundos |
| `"error"` | Timeout ou erro de conexão |
| `"unconfigured"` | `EVOLUTION_BASE_URL` não está definida (apenas campo `evolution`) |

### Como exibir no dashboard

Renderize um semáforo por dependência:

| Estado | Cor | Ícone |
|---|---|---|
| `"ok"` | Verde `#22c55e` | ✓ Operacional |
| `"error"` | Vermelho `#ef4444` | ✗ Falha |
| `"unconfigured"` | Cinza `#6b7280` | — Não configurado |

**Polling recomendado:** 15 segundos. Se status mudar para `"degraded"`, exibir banner de alerta global no topo do dashboard.

---

## 6. Métricas Operacionais

### `GET /api/v1/admin/metrics`

**Autenticação:** `Authorization: Bearer <ADMIN_API_KEY>`

**Timeout:** 5 segundos (inclui queries ao banco)

**Polling recomendado:**
- `retry_queue_size`: a cada 10 segundos
- Demais campos: a cada 30 segundos

### Response 200

```json
{
  "traffic": {
    "inbound_total": 1420,
    "inbound_success": 1398,
    "inbound_error": 22,
    "inbound_error_rate": 1.55,
    "outbound_total": 312,
    "outbound_success": 308,
    "outbound_error": 4,
    "outbound_error_rate": 1.28
  },
  "errors": {
    "error_401_count": 3,
    "error_404_count": 7
  },
  "queues": {
    "retry_queue_size": 2,
    "worker_count": 10
  },
  "runtime": {
    "goroutines_active": 24,
    "memory_alloc_mb": 18.4,
    "memory_sys_mb": 42.1,
    "uptime_seconds": 86400
  },
  "business": {
    "tenants_active": 5,
    "top_tenants": [
      {"id": "uuid-1", "name": "Alpha",   "messages_total": 9820},
      {"id": "uuid-2", "name": "Beta",    "messages_total": 5310},
      {"id": "uuid-3", "name": "Gamma",   "messages_total": 2100},
      {"id": "uuid-4", "name": "Delta",   "messages_total": 870},
      {"id": "uuid-5", "name": "Epsilon", "messages_total": 120}
    ]
  }
}
```

### Referência completa dos 18 campos

#### Seção `traffic`

| Campo | Tipo | Unidade | Threshold de alerta |
|---|---|---|---|
| `inbound_total` | `int64` | mensagens | — (contador cumulativo) |
| `inbound_success` | `int64` | mensagens | — |
| `inbound_error` | `int64` | mensagens | — |
| `inbound_error_rate` | `float64` | % | 🟡 > 2% / 🔴 > 5% |
| `outbound_total` | `int64` | mensagens | — |
| `outbound_success` | `int64` | mensagens | — |
| `outbound_error` | `int64` | mensagens | — |
| `outbound_error_rate` | `float64` | % | 🟡 > 2% / 🔴 > 5% |

> **Atenção:** todos os contadores de tráfego são **em memória** e **resetam a zero** quando o container é reiniciado. Não representam histórico total — representam o período desde o último boot.

#### Seção `errors`

| Campo | Tipo | Descrição | Threshold |
|---|---|---|---|
| `error_401_count` | `int64` | Rejeições HMAC desde o boot (webhook com assinatura inválida) | 🟡 > 0 (investigar) |
| `error_404_count` | `int64` | Falhas de lookup de tenant no fluxo outbound (conta Chatwoot não mapeada no bridge) | 🟡 > 0 (indica tenant sem registro) |

#### Seção `queues`

| Campo | Tipo | Descrição | Threshold |
|---|---|---|---|
| `retry_queue_size` | `int64` | Jobs aguardando reprocessamento no Redis (`retry:queue` sorted set) | 🟡 > 5 / 🔴 > 20 (Evolution GO com problema) |
| `worker_count` | `int` | Número de workers de fila configurados (variável `QUEUE_WORKERS`, default 10) | — (configuração, não alerta) |

#### Seção `runtime`

| Campo | Tipo | Unidade | Threshold |
|---|---|---|---|
| `goroutines_active` | `int` | goroutines | 🟡 > 100 / 🔴 > 500 (possível goroutine leak) |
| `memory_alloc_mb` | `float64` | MB | 🟡 > 100 / 🔴 > 300 |
| `memory_sys_mb` | `float64` | MB | — (informativo) |
| `uptime_seconds` | `int64` | segundos | — (informativo) |

**Converter `uptime_seconds` para exibição:**
```js
function formatUptime(s) {
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  return `${d}d ${h}h ${m}m`
}
```

#### Seção `business`

| Campo | Tipo | Descrição | Fonte |
|---|---|---|---|
| `tenants_active` | `int64` | `COUNT(*) FROM tenants WHERE status = 'active'` | PostgreSQL (real-time) |
| `top_tenants` | `array` | Top 5 tenants ativos por volume total de mensagens | PostgreSQL (real-time) |
| `top_tenants[].id` | `string` | UUID do tenant | — |
| `top_tenants[].name` | `string` | Nome do tenant | — |
| `top_tenants[].messages_total` | `int64` | Total acumulado de mensagens (persistido — não reseta no restart) | — |

> **Diferença importante:** `traffic.inbound_total` conta só desde o último restart; `top_tenants[].messages_total` conta desde sempre (gravado no banco).

### Sugestão de cards e gráficos

| Card | Fonte | Visualização | Cor de alerta |
|---|---|---|---|
| Mensagens Inbound | `traffic.inbound_total` | Contador grande | — |
| Taxa de Sucesso Inbound | `100 - inbound_error_rate` | Gauge circular | < 95% → amarelo, < 90% → vermelho |
| Mensagens Outbound | `traffic.outbound_total` | Contador grande | — |
| Taxa de Sucesso Outbound | `100 - outbound_error_rate` | Gauge circular | < 95% → amarelo, < 90% → vermelho |
| Fila de Retry | `queues.retry_queue_size` | Badge numérico | > 0 → amarelo, > 20 → vermelho |
| Erros de Auth (401) | `errors.error_401_count` | Badge numérico | > 0 → amarelo |
| Tenants Ativos | `business.tenants_active` | Contador | — |
| Uptime | `runtime.uptime_seconds` | Texto formatado | — |
| Memória | `runtime.memory_alloc_mb` | Barra de progresso | > 100MB → amarelo |
| Goroutines | `runtime.goroutines_active` | Contador | > 100 → vermelho |
| Ranking por Volume | `business.top_tenants` | Tabela ou bar chart | — |
| Health das Deps | `/health/ready` | Semáforo 3 itens | qualquer "error" → vermelho global |

---

## 7. Gestão Centralizada por Tenant

Página de detalhes de um tenant com três abas.

---

### Tab 1 — Configurações & Identidade

**Fonte:** `GET /api/v1/admin/tenants/{id}`

#### Campos somente leitura (exibir, não editar)

| Campo | Exibição |
|---|---|
| `id` | Campo copiável com botão "Copiar UUID" |
| `chatwoot_account_id` | Link clicável → `https://mobiochat.com/app/accounts/{id}` |
| `evolution_default_instance` | Texto simples — nome da instância WhatsApp |
| `created_at` | Data formatada |
| `updated_at` | Data formatada + "há X minutos" relativo |

#### Campos editáveis via ações específicas

**Trocar plano:**

```
PUT /api/v1/admin/tenants/{id}/plan
Authorization: Bearer <ADMIN_API_KEY>
Content-Type: application/json

{"plan_id": 3}
```

Response 200:
```json
{"status": "upgraded"}
```

| HTTP | `code` | Causa |
|---|---|---|
| `400` | `INVALID_PAYLOAD` | `plan_id` ausente ou não numérico |
| `500` | `UPGRADE_FAILED` | Plano não encontrado no banco |

**Atualizar Chatwoot Webhook Secret manualmente:**

Usado apenas em situações emergenciais (ex: o secret divergiu entre o Chatwoot e o banco).

```
PUT /api/v1/admin/tenants/{id}/chatwoot-webhook-secret
Authorization: Bearer <ADMIN_API_KEY>
Content-Type: application/json

{"secret": "novo-secret-gerado-pelo-chatwoot"}
```

Response 200:
```json
{"status": "updated"}
```

| HTTP | `code` | Causa |
|---|---|---|
| `400` | `INVALID_BODY` | `secret` vazio ou body inválido |
| `404` | `NOT_FOUND` | Tenant não encontrado |
| `500` | `UPDATE_FAILED` | Erro ao gravar no banco |

#### Status de Webhooks

Exibir dois badges:

```
Webhook Evolution:  [🟢 Configurado] ou [🔴 Não configurado]
Webhook Chatwoot:   [🟢 Configurado] ou [🔴 Não configurado]
```

Se algum estiver `false`, mostrar botão de ação correspondente (ver Tab 3).

---

### Tab 2 — Histórico & Volume

**Fonte:** `GET /api/v1/admin/metrics` (campo `business.top_tenants`)

O endpoint de métricas não filtra por tenant individualmente — retorna o top 5 global. Para detalhes por tenant, use:

```
GET /api/v1/admin/tenants/{id}/usage
Authorization: Bearer <ADMIN_API_KEY>
```

**Response 200:**
```json
{
  "usage": {
    "id": "uuid-interno",
    "tenant_id": "550e8400-...",
    "plan_id": 2,
    "agents_used": 3,
    "inboxes_used": 1,
    "instances_used": 1,
    "messages_today": 47,
    "messages_total": 9820,
    "contacts_used": 312,
    "conversations_used": 284,
    "message_day_reset": "2026-05-04T00:00:00Z",
    "created_at": "2025-11-01T14:30:00Z",
    "updated_at": "2026-05-04T15:22:00Z"
  },
  "plan": {
    "id": 2,
    "name": "starter",
    "display_name": "Starter",
    "max_agents": 5,
    "max_inboxes": 3,
    "max_instances": 2,
    "max_messages_per_day": 500,
    "max_contacts": 1000,
    "enable_templates": true,
    "enable_webhooks": true,
    "enable_analytics": false,
    "enable_sla_alerts": false,
    "enable_custom_domain": false,
    "data_retention_days": 90,
    "created_at": "2025-10-01T00:00:00Z"
  }
}
```

**Campos de uso por tenant:**

| Campo | Tipo | Descrição |
|---|---|---|
| `messages_today` | `int` | Mensagens processadas hoje (reseta à meia-noite) |
| `messages_total` | `int` | Total histórico acumulado |
| `contacts_used` | `int` | Contatos únicos registrados |
| `conversations_used` | `int` | Conversas abertas até hoje |
| `agents_used` | `int` | Agentes Chatwoot associados à conta |
| `inboxes_used` | `int` | Inboxes configuradas |
| `instances_used` | `int` | Instâncias Evolution GO provisionadas |
| `message_day_reset` | `ISO 8601` | Quando `messages_today` foi zerado pela última vez |

**Barras de uso (comparar com limites do plano):**

```
Mensagens hoje:  [████████░░░░] 47 / 500
Contatos:        [███░░░░░░░░░] 312 / 1000
Agentes:         [██░░░░░░░░░░] 3 / 5
```

**Polling recomendado para atualização em tempo real:** 30 segundos.

---

### Tab 3 — Ações Avançadas (Manutenção)

Cada ação nesta tab exige confirmação explícita do usuário antes de enviar.

---

#### Ação: Reconfigurar webhook Evolution

**Quando usar:** Quando `webhook_evolution_configured = false` ou quando mensagens inbound pararam de chegar sem motivo aparente. O webhook health checker do bridge tenta fazer isso automaticamente a cada 10 minutos, mas esta ação força o processo imediatamente.

```
POST /api/v1/admin/tenants/{id}/connect
Authorization: Bearer <ADMIN_API_KEY>
Content-Type: application/json

{}
```

Body é opcional. Se quiser atualizar o token da instância ao mesmo tempo:
```json
{"token": "novo-token-da-instancia-evolution"}
```

**Response 200:**
```json
{"status": "connected"}
```

| HTTP | `code` | Causa |
|---|---|---|
| `500` | `CONNECT_FAILED` | Instância não encontrada no banco ou falha na Evolution GO |

**Confirmação sugerida:** "Isso irá reconfigurar o webhook na Evolution GO para esta instância. Continuar?"

---

#### Ação: Sincronizar Chatwoot Webhook Secret

**Quando usar:** Quando `webhook_chatwoot_configured = false`, ou quando o HMAC de webhooks do Chatwoot está falhando. Busca o secret real diretamente do Chatwoot e atualiza o banco do bridge.

```
POST /api/v1/admin/tenants/{id}/sync-chatwoot-secret
Authorization: Bearer <ADMIN_API_KEY>
```

Sem body.

**Response 200:**
```json
{"status": "synced", "secret_len": 64}
```

O campo `secret_len` indica o tamanho do secret sincronizado (para confirmar que não está vazio).

| HTTP | `code` | Causa |
|---|---|---|
| `500` | `SYNC_FAILED` | Tenant sem `chatwoot_api_token`, ou inbox não encontrada no Chatwoot |

**Confirmação sugerida:** "O secret atual do banco será substituído pelo valor atual no Chatwoot. Continuar?"

---

#### Ação: Remover Tenant

**Quando usar:** Offboarding completo. Remove apenas o registro do banco — NÃO deleta a conta no Chatwoot nem a instância na Evolution GO.

```
DELETE /api/v1/admin/tenants/{id}
Authorization: Bearer <ADMIN_API_KEY>
```

**Response 200:**
```json
{"status": "deleted"}
```

| HTTP | `code` | Causa |
|---|---|---|
| `500` | `DELETE_FAILED` | Erro de banco (FK violation, registro não encontrado) |

**Confirmação sugerida (dupla confirmação):**  
Passo 1: "Tem certeza que deseja remover o tenant `{name}`?"  
Passo 2: "Digite o nome do tenant para confirmar: `____`"

> **Atenção:** esta ação não remove dados do Chatwoot nem da Evolution GO. Webhooks continuarão chegando mas serão rejeitados com 401 (tenant não encontrado). Faça o offboarding externo separadamente.

---

## 8. Lifecycle — Estratégia de Desativação

### Como desativar um tenant sem deletar

O bridge não tem um endpoint `PATCH /tenants/{id}/status` nesta versão. A estratégia de desativação suave é feita via plano:

**Opção 1 — Trocar para plano zero/restrito:**
```
PUT /api/v1/admin/tenants/{id}/plan
{"plan_id": 0}
```
O `QuotaService` passará a bloquear operações acima dos limites (0 mensagens/dia = sem tráfego).

**Opção 2 — Deletar o tenant do bridge (offboarding duro):**
```
DELETE /api/v1/admin/tenants/{id}
```
Todos os webhooks subsequentes receberão 401 e serão descartados.

### Como reativar um tenant

Se o tenant ainda existe no banco com `status = "active"` mas os webhooks estão quebrados:

1. `POST /api/v1/admin/tenants/{id}/connect` — reconecta webhook Evolution
2. `POST /api/v1/admin/tenants/{id}/sync-chatwoot-secret` — ressincroniza secret Chatwoot
3. Verificar `GET /api/v1/admin/tenants/{id}` → ambos os campos `webhook_*_configured` devem ser `true`

Se o tenant foi deletado do banco: reprovisionar com `POST /api/v1/admin/tenants` usando o mesmo `workspace_id`. O mecanismo de idempotência verificará os serviços externos e recriará apenas o que estiver faltando.

### Fluxo de offboarding completo

Ordem recomendada de operações:

```
1. Notificar o cliente (fora do bridge)
2. PUT /api/v1/admin/tenants/{id}/plan  → plan_id: 0  (para tráfego)
3. Aguardar drenagem de mensagens em andamento (30–60 segundos)
4. DELETE /api/v1/admin/tenants/{id}   (remove do bridge)
5. [Manual] Deletar conta no Chatwoot via painel admin
6. [Manual] Deletar instância na Evolution GO via painel admin
```

### O que acontece com mensagens em trânsito durante a desativação

- **Mensagens inbound (WhatsApp → bridge):** Se o tenant ainda existe no banco, são processadas normalmente até o DELETE. Após o DELETE, o webhook da Evolution GO continuará chegando mas retornará 404 (tenant não encontrado) e será descartado. A Evolution GO não reenvia webhooks descartados.
- **Mensagens outbound (Chatwoot → bridge):** O webhook do Chatwoot usa `?tenant={id}` na URL. Após o DELETE, retorna 401. O Chatwoot registra o erro mas não reenvia.
- **Retry queue:** Jobs em retry no Redis que ainda estiverem na fila continuarão sendo processados até expirarem ou terem o worker interrompido.

### Como o webhook health checker reage a tenants desativados

O health checker (`RunWebhookHealthChecker`) roda a cada 10 minutos (configurável via `WEBHOOK_HEALTH_CHECK_INTERVAL`) e verifica se o webhook ainda está configurado na Evolution GO para todas as instâncias no banco. Se o webhook estiver ausente, tenta `ReconnectInstance` automaticamente.

Tenants deletados do banco não aparecem na listagem de instâncias, portanto o health checker os ignora automaticamente.

---

## 9. Exemplos Completos de Request/Response

### Criar tenant — Sucesso

```bash
curl -X POST https://bridge.mobiochat.com/api/v1/admin/tenants \
  -H "Authorization: Bearer minha-chave-admin" \
  -H "Content-Type: application/json" \
  -d '{
    "workspace_id": "550e8400-e29b-41d4-a716-446655440000",
    "name": "Empresa Alpha",
    "domain": "alpha.com",
    "external_id": "erp-0042",
    "plan_id": 1,
    "admin_email": "admin@alpha.com",
    "admin_password": "senha-segura-123",
    "instance_name": "alpha-whatsapp"
  }'
```

**Response 201:**
```json
{
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "chatwoot_account_id": 17,
  "chatwoot_inbox_id": 5,
  "evolution_instance_id": "alpha-whatsapp-a3f2e1",
  "qr_code": "",
  "status": "active",
  "correlation_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

---

### Criar tenant — Campos obrigatórios ausentes

```bash
curl -X POST https://bridge.mobiochat.com/api/v1/admin/tenants \
  -H "Authorization: Bearer minha-chave-admin" \
  -H "Content-Type: application/json" \
  -d '{"name": "Alpha"}'
```

**Response 400:**
```json
{
  "error": "name, admin_email and admin_password are required",
  "code": "MISSING_FIELDS",
  "details": null
}
```

---

### Buscar tenant

```bash
curl https://bridge.mobiochat.com/api/v1/admin/tenants/550e8400-e29b-41d4-a716-446655440000 \
  -H "Authorization: Bearer minha-chave-admin"
```

**Response 200:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "Empresa Alpha",
  "domain": "alpha.com",
  "external_id": "erp-0042",
  "status": "active",
  "plan_id": 1,
  "chatwoot_account_id": 17,
  "chatwoot_account_name": null,
  "chatwoot_api_token": "xBZ9kP...",
  "evolution_base_url": "https://evo.mobiochat.com",
  "evolution_default_instance": "alpha-whatsapp-a3f2e1",
  "webhook_evolution_configured": true,
  "webhook_chatwoot_configured": true,
  "chatwoot_webhook_secret": "whsec_...",
  "metadata": null,
  "created_at": "2026-05-04T14:30:00Z",
  "updated_at": "2026-05-04T14:30:12Z"
}
```

---

### Buscar tenant — Não encontrado

**Response 404:**
```json
{
  "error": "Tenant not found",
  "code": "TENANT_NOT_FOUND",
  "details": null
}
```

---

### Health Ready — Degradado

```bash
curl https://bridge.mobiochat.com/health/ready
```

**Response 503:**
```json
{
  "status": "degraded",
  "dependencies": {
    "postgres": "ok",
    "redis": "error",
    "evolution": "ok"
  }
}
```

---

### Métricas — Auth inválida

```bash
curl https://bridge.mobiochat.com/api/v1/admin/metrics
```
(sem header Authorization)

**Response 401:**
```json
{
  "error": "unauthorized",
  "code": "INVALID_API_KEY"
}
```

---

### Trocar plano

```bash
curl -X PUT https://bridge.mobiochat.com/api/v1/admin/tenants/550e8400-.../plan \
  -H "Authorization: Bearer minha-chave-admin" \
  -H "Content-Type: application/json" \
  -d '{"plan_id": 2}'
```

**Response 200:**
```json
{"status": "upgraded"}
```

---

### Reconnect webhook Evolution

```bash
curl -X POST https://bridge.mobiochat.com/api/v1/admin/tenants/550e8400-.../connect \
  -H "Authorization: Bearer minha-chave-admin" \
  -H "Content-Type: application/json" \
  -d '{}'
```

**Response 200:**
```json
{"status": "connected"}
```

---

### Sincronizar secret Chatwoot

```bash
curl -X POST https://bridge.mobiochat.com/api/v1/admin/tenants/550e8400-.../sync-chatwoot-secret \
  -H "Authorization: Bearer minha-chave-admin"
```

**Response 200:**
```json
{"status": "synced", "secret_len": 64}
```

---

### Deletar tenant

```bash
curl -X DELETE https://bridge.mobiochat.com/api/v1/admin/tenants/550e8400-... \
  -H "Authorization: Bearer minha-chave-admin"
```

**Response 200:**
```json
{"status": "deleted"}
```

---

## 10. Glossário

| Termo | Definição |
|---|---|
| **tenant** | Um cliente da plataforma. Cada tenant tem conta Chatwoot, instância Evolution GO e registro no banco do bridge isolados dos demais. |
| **workspace_id** | UUID que identifica o tenant no bridge. Pode ser o mesmo ID usado no seu sistema de cobrança/ERP. Se omitido no provisionamento, é gerado automaticamente. Equivale ao `tenant_id` no response. |
| **instance** (Evolution GO) | Uma sessão WhatsApp Web. Cada instância tem seu próprio número de telefone, QR code de conexão e token de API. Uma instância = uma linha WhatsApp. |
| **inbox** (Chatwoot) | Canal de entrada de mensagens no Chatwoot. No contexto do bridge, cada tenant tem uma inbox do tipo `api` que recebe mensagens vindas do WhatsApp. |
| **chatwoot_account_id** | ID numérico da conta no Chatwoot. Diferente do `tenant_id` do bridge. Usado para construir links diretos: `https://mobiochat.com/app/accounts/{chatwoot_account_id}`. |
| **tenant_id** | UUID interno do bridge (campo `id` no modelo Tenant). É o identificador primário para todas as chamadas de API do dashboard. |
| **evolution_instance** | Nome da instância na Evolution GO (ex: `alpha-whatsapp-a3f2e1`). Gravado em `evolution_default_instance` no tenant e em `custom_attributes.evolution_instance` de cada conversa no Chatwoot. |
| **LID** | _Linked Device Identifier_ — identificador de dispositivo do WhatsApp Web usado em grupos e em alguns contextos de mensagem direta. Quando o `Chat` de uma mensagem termina em `@lid`, o bridge usa o campo `Sender` (que contém o JID real do contato) para extrair o número de telefone. |
| **HMAC** | _Hash-based Message Authentication Code_. Mecanismo de assinatura dos webhooks. O Chatwoot assina o body com HMAC-SHA256 usando o `chatwoot_webhook_secret`; o bridge valida a assinatura antes de processar qualquer evento. |
| **dedup** | Deduplicação por `message_id`. O bridge usa Redis (TTL 24h) para descartar webhooks duplicados da Evolution GO antes de processar. |
| **retry queue** | Fila Redis (`retry:queue`) onde jobs que falharam na Evolution GO ficam aguardando reprocessamento com backoff exponencial. Monitorada via `queues.retry_queue_size`. |
| **circuit breaker** | Mecanismo que detecta falhas repetidas na Evolution GO e pausa envios temporariamente (janela de 30 segundos) para evitar sobrecarga. |
