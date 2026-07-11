ALTER TABLE evolution_instances
    ADD COLUMN IF NOT EXISTS connected_at TIMESTAMP WITH TIME ZONE,
    ADD COLUMN IF NOT EXISTS disconnected_at TIMESTAMP WITH TIME ZONE,
    ADD COLUMN IF NOT EXISTS disconnect_reason VARCHAR(255);

UPDATE evolution_instances SET connected_at = last_connection WHERE last_connection IS NOT NULL;

ALTER TABLE evolution_instances DROP COLUMN IF EXISTS last_connection;
