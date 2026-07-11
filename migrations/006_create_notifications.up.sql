CREATE TABLE IF NOT EXISTS notifications (
    id UUID PRIMARY KEY,
    tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
    idempotency_key VARCHAR(255),
    to_number VARCHAR(50) NOT NULL,
    type VARCHAR(50) NOT NULL,
    content JSONB NOT NULL,
    status VARCHAR(50) DEFAULT 'queued',
    evolution_message_id VARCHAR(255),
    error_message TEXT,
    sent_at TIMESTAMP WITH TIME ZONE,
    delivered_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_notifications_idempotency_key ON notifications(idempotency_key);
CREATE INDEX idx_notifications_tenant_status ON notifications(tenant_id, status);
