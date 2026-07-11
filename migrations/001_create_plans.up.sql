CREATE TABLE IF NOT EXISTS plans (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE,
    display_name VARCHAR(255) NOT NULL,
    max_agents INT NOT NULL DEFAULT 1,
    max_inboxes INT NOT NULL DEFAULT 1,
    max_instances INT NOT NULL DEFAULT 1,
    max_messages_per_day INT NOT NULL DEFAULT 50,
    max_contacts INT NOT NULL DEFAULT 100,
    enable_templates BOOLEAN NOT NULL DEFAULT false,
    enable_webhooks BOOLEAN NOT NULL DEFAULT false,
    enable_analytics BOOLEAN NOT NULL DEFAULT false,
    enable_sla_alerts BOOLEAN NOT NULL DEFAULT false,
    enable_custom_domain BOOLEAN NOT NULL DEFAULT false,
    data_retention_days INT NOT NULL DEFAULT 30,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO plans (
    id, name, display_name, max_agents, max_inboxes, max_instances, max_messages_per_day, max_contacts
) VALUES 
    (1, 'free', 'Free', 1, 1, 1, 50, 100),
    (2, 'pro', 'Pro', 5, 3, 2, 500, 1000),
    (3, 'enterprise', 'Enterprise', 20, 10, 5, 5000, 10000)
ON CONFLICT (id) DO NOTHING;
