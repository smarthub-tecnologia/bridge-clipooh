ALTER TABLE evolution_instances
    DROP COLUMN IF EXISTS chatwoot_inbox_id,
    DROP COLUMN IF EXISTS chatwoot_inbox_name,
    DROP COLUMN IF EXISTS chatwoot_inbox_webhook_secret,
    DROP COLUMN IF EXISTS chatwoot_inbox_identifier;
