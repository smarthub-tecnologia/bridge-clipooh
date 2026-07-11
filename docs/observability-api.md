# Observabilidade — mobio-bridge-go

Referência completa dos endpoints de health check e métricas operacionais.

---

## Endpoints

### 1. GET /health/live

**Propósito:** Confirma que o processo Go está vivo e respondendo. Não verifica nenhuma dependência externa. Usada como *liveness probe* pelo orquestrador — se falhar, o container é reiniciado.

**Autenticação:** Nenhuma. Rota pública.

**Request:**
```
GET /health/live
```

**Response — 200 OK (sempre, enquanto o processo estiver vivo):**
```json
{"status": "live"}
```

**Não há response de erro.** Se o processo estiver morto, a conexão será recusada pelo orquestrador antes de chegar à rota.

**Quando usar:**
- Configurar como liveness probe no EasyPanel / Kubernetes
- Monitoramento de uptime simples (pingdom, UptimeRobot)
- Verificar se o deploy subiu corretamente

---

### 2. GET /health/ready

**Propósito:** Verifica se o serviço está apto a receber tráfego. Pinga PostgreSQL, Redis e Evolution GO com timeout de 3 segundos cada. Se qualquer dependência falhar, retorna `503` e o EasyPanel para de enviar tráfego para o container.

**Autenticação:** Nenhuma. Rota pública.

**Request:**
```
GET /health/ready
```

**Response — 200 OK (todas as dependências saudáveis):**
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

**Response — 503 Service Unavailable (uma ou mais dependências com falha):**
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

**Valores possíveis por dependência:**

| Valor | Significado |
|---|---|
| `"ok"` | Dependência respondeu dentro do timeout de 3s |
| `"error"` | Timeout ou erro de conexão |
| `"unconfigured"` | `EVOLUTION_BASE_URL` não configurada (apenas para `evolution`) |

**Lógica de verificação:**
- **postgres** — `pgxpool.Pool.Ping()` com context de 3s
- **redis** — `PING` com context de 3s
- **evolution** — `GET {EVOLUTION_BASE_URL}/health`; qualquer status HTTP < 500 é considerado `"ok"`

**Quando usar:**
- Configurar como readiness probe no EasyPanel
- Monitorar saúde das dependências em dashboards
- Detectar degradação antes de impacto em usuários

---

### 3. GET /api/v1/admin/metrics

**Propósito:** Retorna snapshot completo de métricas operacionais do serviço: tráfego de mensagens, erros, filas, runtime Go e dados de negócio. Contadores de tráfego são **em memória** — resetam ao reiniciar o container.

**Autenticação:** Header `X-Admin-API-Key: {ADMIN_API_KEY}` obrigatório.

**Request:**
```
GET /api/v1/admin/metrics
X-Admin-API-Key: sua-chave-admin
```

**Response — 200 OK:**
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
      {"id": "uuid-1", "name": "Tenant Alpha", "messages_total": 9820},
      {"id": "uuid-2", "name": "Tenant Beta",  "messages_total": 5310},
      {"id": "uuid-3", "name": "Tenant Gamma", "messages_total": 2100},
      {"id": "uuid-4", "name": "Tenant Delta", "messages_total": 870},
      {"id": "uuid-5", "name": "Tenant Epsilon","messages_total": 120}
    ]
  }
}
```

**Response — 401 Unauthorized:**
```json
{"error": "unauthorized", "code": "INVALID_API_KEY"}
```

**Quando usar:**
- Polling periódico por dashboard administrativo
- Alertas de erro_rate acima de threshold
- Verificar backlog de retry antes de intervenção manual

---

## Campos de métricas — referência completa

### `traffic`

| Campo | Tipo | Descrição |
|---|---|---|
| `inbound_total` | int | Total de mensagens recebidas da Evolution GO (WhatsApp → Chatwoot). Contador em memória, reseta no restart. |
| `inbound_success` | int | Mensagens inbound encaminhadas ao Chatwoot com sucesso. |
| `inbound_error` | int | Mensagens inbound que falharam antes de chegar ao Chatwoot. |
| `inbound_error_rate` | float | `(inbound_error / inbound_total) * 100`. Retorna `0` se `inbound_total == 0`. |
| `outbound_total` | int | Total de mensagens recebidas do Chatwoot (agente → WhatsApp). Apenas eventos `outgoing` não-privados. |
| `outbound_success` | int | Mensagens outbound enviadas ao WhatsApp com sucesso via Evolution GO. |
| `outbound_error` | int | Mensagens outbound que falharam no envio. |
| `outbound_error_rate` | float | `(outbound_error / outbound_total) * 100`. |

### `errors`

| Campo | Tipo | Descrição |
|---|---|---|
| `error_401_count` | int | Total de rejeições HMAC desde o último restart — inclui webhooks da Evolution e do Chatwoot com assinatura inválida. |
| `error_404_count` | int | Total de falhas de lookup de tenant por `chatwoot_account_id` no fluxo outbound. Indica mensagens chegando de contas Chatwoot não mapeadas no bridge. |

### `queues`

| Campo | Tipo | Descrição |
|---|---|---|
| `retry_queue_size` | int | Número de jobs atualmente na fila de retry do Redis (`retry:queue` sorted set). Crescimento contínuo indica problema persistente com Evolution GO. |
| `worker_count` | int | Valor de `QUEUE_WORKERS` configurado (default: 10). Número de workers de fila iniciados no startup. |

### `runtime`

| Campo | Tipo | Descrição |
|---|---|---|
| `goroutines_active` | int | Goroutines ativas no momento da requisição. Crescimento anormal pode indicar goroutine leak. Baseline típico: 15–30. |
| `memory_alloc_mb` | float | Heap alocado em uso (MB). Medido por `runtime.MemStats.Alloc`. Valores esperados: 10–50 MB em operação normal. |
| `memory_sys_mb` | float | Total de memória obtida do SO (MB), inclui heap + stack + runtime overhead. |
| `uptime_seconds` | int | Segundos desde o startup do processo. |

### `business`

| Campo | Tipo | Descrição |
|---|---|---|
| `tenants_active` | int | `COUNT(*)` de tenants com `status = 'active'` no PostgreSQL. Consultado em tempo real a cada requisição. |
| `top_tenants` | array | Top 5 tenants ordenados por `messages_total` desc (campo da tabela `tenant_usage`). Inclui apenas tenants ativos. |
| `top_tenants[].id` | string | UUID do tenant. |
| `top_tenants[].name` | string | Nome do tenant. |
| `top_tenants[].messages_total` | int | Total acumulado de mensagens processadas (persistido em banco — não reseta no restart). |

---

## Integração com EasyPanel

Configure dois health checks no painel do serviço `mobio-bridge-go`:

**Liveness probe** (reinicia o container se falhar):
```
Path:               /health/live
Method:             GET
Expected status:    200
Interval:           30s
Timeout:            5s
Failure threshold:  3
```

**Readiness probe** (retira do load balancer se degradado):
```
Path:               /health/ready
Method:             GET
Expected status:    200
Interval:           15s
Timeout:            5s
Failure threshold:  2
```

> **Nota:** Ambas as rotas são públicas. Não é necessário configurar headers de autenticação nas probes.

Se o EasyPanel não suportar readiness probe separada, use apenas `/health/live` como health check principal e monitore `/health/ready` externamente via UptimeRobot ou similar.

---

## Integração com Dashboard

Campos recomendados para cada card da UI administrativa:

| Card | Campo | Formato sugerido |
|---|---|---|
| **Mensagens hoje (inbound)** | `traffic.inbound_total` | contador |
| **Taxa de sucesso inbound** | `100 - traffic.inbound_error_rate` | `%` com 1 decimal |
| **Mensagens enviadas (outbound)** | `traffic.outbound_total` | contador |
| **Taxa de sucesso outbound** | `100 - traffic.outbound_error_rate` | `%` com 1 decimal |
| **Fila de retry** | `queues.retry_queue_size` | badge vermelho se > 0 |
| **Erros de autenticação** | `errors.error_401_count` | badge amarelo se > 0 |
| **Tenants ativos** | `business.tenants_active` | contador |
| **Uptime** | `runtime.uptime_seconds` | converter para `Xd Xh Xm` |
| **Memória** | `runtime.memory_alloc_mb` | `X MB` |
| **Goroutines** | `runtime.goroutines_active` | alerta se > 100 |
| **Status das dependências** | `dependencies.*` de `/health/ready` | semáforo verde/vermelho |

**Polling recomendado:** 30 segundos para métricas de tráfego; 10 segundos para `retry_queue_size` e health status.

**Alerta sugerido:** `inbound_error_rate > 5%` ou `outbound_error_rate > 5%` por mais de 2 ciclos consecutivos indica problema a investigar.
