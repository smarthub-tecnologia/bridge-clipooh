DROP INDEX IF EXISTS idx_tenants_workspace_id;
ALTER TABLE tenants DROP COLUMN IF EXISTS workspace_id;
