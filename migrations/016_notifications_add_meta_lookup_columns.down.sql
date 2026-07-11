DROP INDEX IF EXISTS idx_notifications_instance_to;
ALTER TABLE notifications DROP COLUMN IF EXISTS instance_name;
