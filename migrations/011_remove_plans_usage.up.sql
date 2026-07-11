-- Remove plan_id column and drop usage/plans tables
-- Limits are now enforced by the dashboard (Directus), not the bridge

ALTER TABLE tenants DROP COLUMN IF EXISTS plan_id;
DROP TABLE IF EXISTS tenant_usage CASCADE;
DROP TABLE IF EXISTS plans CASCADE;
