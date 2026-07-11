DROP INDEX IF EXISTS idx_chatwoot_inboxes_provider;
DROP INDEX IF EXISTS idx_chatwoot_inboxes_phone;

ALTER TABLE chatwoot_inboxes
  DROP COLUMN IF EXISTS provider,
  DROP COLUMN IF EXISTS meta_phone_number_id;
