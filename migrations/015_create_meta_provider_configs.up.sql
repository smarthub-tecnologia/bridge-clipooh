-- Traz meta_provider_configs pro Postgres proprio do bridge — antes vivia no
-- Supabase da plataforma (SUPABASE_DATABASE_URL) com fallback via Directus
-- REST. Schema adaptado da migration 002 da plataforma (cartaopro-linkkopro),
-- sem as colunas dependentes de conceito de workspace (RLS por workspace nao
-- se aplica aqui — instalacao interna, single-tenant do ponto de vista do
-- Chatwoot/Meta).
--
-- evolution_instance_id guarda o NOME legivel da instancia (o mesmo valor
-- usado em evolution_instances.instance_name e no ?instance= dos webhooks
-- Evolution), nao o UUID interno — mantem a mesma convencao que o resto do
-- codigo (webhook.InstanceName) e evita um hop de resolucao UUID que so
-- existia porque o schema original vivia em outro banco.
CREATE TABLE IF NOT EXISTS meta_provider_configs (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    evolution_instance_id   VARCHAR(255) NOT NULL,
    wa_business_account_id  VARCHAR(255) NOT NULL,
    wa_phone_number_id      VARCHAR(255) NOT NULL,
    wa_access_token_enc     TEXT NOT NULL,
    token_key_version       VARCHAR(10) NOT NULL DEFAULT 'v1',
    wa_language              VARCHAR(10) DEFAULT 'pt_BR',
    wa_waba_name             VARCHAR(255),
    webhook_verify_token     VARCHAR(64),
    is_active                BOOLEAN NOT NULL DEFAULT true,
    last_validated_at        TIMESTAMP WITH TIME ZONE,
    created_at               TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    updated_at               TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
);

-- Cada phone_number_id so pode estar vinculado a uma config ativa por vez —
-- chave de lookup primaria pras mensagens inbound da Meta.
CREATE UNIQUE INDEX idx_mpc_phone_unique ON meta_provider_configs (wa_phone_number_id);
CREATE INDEX idx_mpc_instance ON meta_provider_configs (evolution_instance_id);
