package models

import (
	"os"
	"time"
)

type Tenant struct {
	ID                         string                 `json:"id"`
	WorkspaceID                *string                `json:"workspace_id,omitempty"`
	Name                       string                 `json:"name"`
	Domain                     *string                `json:"domain,omitempty"`
	ExternalID                 *string                `json:"external_id,omitempty"`
	Status                     string                 `json:"status"`
	ChatwootAccountID          *int                   `json:"chatwoot_account_id,omitempty"`
	ChatwootAccountName        *string                `json:"chatwoot_account_name,omitempty"`
	ChatwootAPIToken           *string                `json:"chatwoot_api_token,omitempty"`
	EvolutionBaseURL           *string                `json:"evolution_base_url,omitempty"`
	EvolutionDefaultInstance   *string                `json:"evolution_default_instance,omitempty"`
	WebhookEvolutionConfigured bool                   `json:"webhook_evolution_configured"`
	WebhookChatwootConfigured  bool                   `json:"webhook_chatwoot_configured"`
	ChatwootWebhookSecret      *string                `json:"chatwoot_webhook_secret,omitempty"`
	Metadata                   map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt                  time.Time              `json:"created_at"`
	UpdatedAt                  time.Time              `json:"updated_at"`
}

func (t *Tenant) ChatwootInternalURL() string {
	// Tenta CHATWOOT_BASE_URL primeiro (URL pública ex: https://mobiochat.com)
	if url := os.Getenv("CHATWOOT_BASE_URL"); url != "" {
		return url
	}
	// Fallback para URL interna (Docker)
	return os.Getenv("CHATWOOT_INTERNAL_URL")
}
