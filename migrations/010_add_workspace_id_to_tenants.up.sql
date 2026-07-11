ALTER TABLE tenants ADD COLUMN IF NOT EXISTS workspace_id UUID;

-- Backfill: for tenants provisioned before this change, workspace_id = id (they were the same UUID)
UPDATE tenants SET workspace_id = id::uuid WHERE workspace_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_tenants_workspace_id ON tenants(workspace_id);
