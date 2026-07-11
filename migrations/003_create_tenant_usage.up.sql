CREATE TABLE IF NOT EXISTS tenant_usage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL UNIQUE REFERENCES tenants(id) ON DELETE CASCADE,
    plan_id INT NOT NULL REFERENCES plans(id),
    agents_used INT DEFAULT 0,
    inboxes_used INT DEFAULT 0,
    instances_used INT DEFAULT 0,
    messages_today INT DEFAULT 0,
    messages_total INT DEFAULT 0,
    contacts_used INT DEFAULT 0,
    conversations_used INT DEFAULT 0,
    message_day_reset TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
