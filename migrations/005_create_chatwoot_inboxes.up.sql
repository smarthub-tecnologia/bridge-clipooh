CREATE TABLE IF NOT EXISTS chatwoot_inboxes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    chatwoot_account_id INT NOT NULL,
    inbox_id INT NOT NULL,
    inbox_name VARCHAR(255) NOT NULL,
    inbox_type VARCHAR(100) DEFAULT 'whatsapp',
    webhook_configured BOOLEAN DEFAULT false,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
