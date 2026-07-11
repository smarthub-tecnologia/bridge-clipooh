ALTER TABLE evolution_instances
    ADD COLUMN IF NOT EXISTS last_connection TIMESTAMP WITH TIME ZONE;

UPDATE evolution_instances SET last_connection = connected_at WHERE connected_at IS NOT NULL;

ALTER TABLE evolution_instances
    DROP COLUMN IF EXISTS connected_at,
    DROP COLUMN IF EXISTS disconnected_at,
    DROP COLUMN IF EXISTS disconnect_reason;
