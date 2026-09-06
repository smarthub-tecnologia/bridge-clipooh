# bridge

Bridge entre Evolution GO (WhatsApp) e Chatwoot: encaminha mensagens inbound do
WhatsApp para a inbox Chatwoot correta e envia de volta as respostas dos
agentes.

- `/webhook/evolution` — eventos da Evolution GO (mensagens, conexão/desconexão,
  QR code). Roteados por instância via `instance_name` do payload.
- `/webhook/chatwoot` — eventos do Chatwoot (respostas de agentes, edições).
  Ver [Validação do webhook do Chatwoot](#validação-do-webhook-do-chatwoot).

## Validação do webhook do Chatwoot

Cada instância WhatsApp cadastrada em `evolution_instances` é vinculada a uma
inbox Chatwoot própria (`chatwoot_inbox_id`, `chatwoot_inbox_webhook_secret`,
preenchidos automaticamente na criação da instância). O `/webhook/chatwoot`
usa esse vínculo para resolver **qual secret validar** a cada request:

1. Extrai `inbox_id` do payload (`inbox.id` ou `conversation.inbox_id`).
2. Busca em `evolution_instances` a linha com aquele `chatwoot_inbox_id`.
3. Se encontrar e ela tiver `chatwoot_inbox_webhook_secret` preenchido, valida
   o HMAC-SHA256 (`X-Chatwoot-Signature`) contra **esse secret**.
4. Caso contrário (instância não encontrada, ou secret não preenchido),
   cai para a env var global `CHATWOOT_WEBHOOK_SECRET` — mantém
   compatibilidade com configs antigas que ainda não têm o secret
   por-instância.

Isso permite múltiplas inboxes Chatwoot (ex: Assistente OOH, Comercial OOH,
Criação OOH) validando webhooks corretamente ao mesmo tempo, sem precisar
trocar a env var manualmente entre elas. `CHATWOOT_WEBHOOK_SECRET` continua
existindo e não deve ser removida do docker-compose/EasyPanel — ela só deixou
de ser a única fonte de verdade.

Cada mismatch de HMAC é logado com a origem do secret usado (`secret_source`:
instância específica ou fallback global) e o prefixo do secret (nunca o
secret completo), para facilitar debug.

Implementação: [internal/services/bridge.go](internal/services/bridge.go)
(`ValidateChatwootWebhookSecret`, `resolveChatwootWebhookSecret`) e
[internal/repository/instance_repo.go](internal/repository/instance_repo.go)
(`FindByChatwootInboxID`).
