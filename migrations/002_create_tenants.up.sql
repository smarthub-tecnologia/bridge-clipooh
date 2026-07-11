CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS tenants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    domain VARCHAR(255),
    external_id VARCHAR(255),
    status VARCHAR(50) DEFAULT 'active',
    plan_id INT REFERENCES plans(id),
    chatwoot_account_id INT,
    chatwoot_account_name VARCHAR(255),
    chatwoot_api_token TEXT,
    evolution_base_url VARCHAR(255),
    evolution_default_instance VARCHAR(255),
    webhook_evolution_configured BOOLEAN DEFAULT false,
    webhook_chatwoot_configured BOOLEAN DEFAULT false,
    metadata JSONB,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
