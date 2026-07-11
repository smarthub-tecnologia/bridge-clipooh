ALTER TABLE chatwoot_inboxes
  ADD COLUMN IF NOT EXISTS provider VARCHAR(20) NOT NULL DEFAULT 'evolution',
  ADD COLUMN IF NOT EXISTS meta_phone_number_id VARCHAR(50) DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_chatwoot_inboxes_provider
  ON chatwoot_inboxes (tenant_id, provider);

CREATE INDEX IF NOT EXISTS idx_chatwoot_inboxes_phone
  ON chatwoot_inboxes (meta_phone_number_id)
  WHERE meta_phone_number_id IS NOT NULL;
