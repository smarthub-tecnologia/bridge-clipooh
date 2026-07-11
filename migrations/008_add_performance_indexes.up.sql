-- tenants: busca por chatwoot_account_id em todo webhook outbound
CREATE INDEX IF NOT EXISTS idx_tenants_chatwoot_account_id
    ON tenants (chatwoot_account_id);

-- evolution_instances: busca por nome (inbound webhook via instance name)
CREATE INDEX IF NOT EXISTS idx_evolution_instances_instance_name
    ON evolution_instances (instance_name);

-- evolution_instances: busca por tenant (FindDefaultByTenantID)
CREATE INDEX IF NOT EXISTS idx_evolution_instances_tenant_id
    ON evolution_instances (tenant_id);

-- evolution_instances: filtro por status (health checker — FindAll)
CREATE INDEX IF NOT EXISTS idx_evolution_instances_status
    ON evolution_instances (status);
