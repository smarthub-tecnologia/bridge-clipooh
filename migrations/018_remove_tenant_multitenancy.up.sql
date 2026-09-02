-- Bridge deixa de ser multi-tenant: instalação single-tenant exclusiva da
-- Cartão Pro. tenant_id/workspace_id somem do schema; a config antes gravada
-- por-tenant (chatwoot_account_id, chatwoot_api_token, chatwoot_webhook_secret)
-- passa a vir de env vars fixas (CHATWOOT_ACCOUNT_ID, CHATWOOT_API_TOKEN,
-- CHATWOOT_WEBHOOK_SECRET). evolution_instances continua 1-para-N (múltiplas
-- linhas WhatsApp seguem existindo), só perde o vínculo de tenant.
--
-- addon_events também sai por completo: existia para alimentar uma
-- plataforma externa (Directus/Linkko) que não existe mais nesta arquitetura.

ALTER TABLE evolution_instances DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE chatwoot_inboxes DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE notifications DROP COLUMN IF EXISTS tenant_id;

DROP TABLE IF EXISTS addon_events;
DROP TABLE IF EXISTS tenants;
