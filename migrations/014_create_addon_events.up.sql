-- Traz addon_events pro Postgres proprio do bridge — antes vivia no banco do
-- Directus da plataforma (DIRECTUS_DATABASE_URL), dependencia que esse bridge
-- interno nao precisa mais ter.
CREATE TABLE IF NOT EXISTS addon_events (
    id           UUID PRIMARY KEY,
    workspace_id UUID,
    resource_id  VARCHAR(255) NOT NULL,
    addon_type   VARCHAR(50)  NOT NULL,
    event_type   VARCHAR(50)  NOT NULL,
    triggered_by VARCHAR(50)  NOT NULL,
    metadata     JSONB,
    date_created TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_addon_events_resource ON addon_events(resource_id);
CREATE INDEX idx_addon_events_workspace ON addon_events(workspace_id);
