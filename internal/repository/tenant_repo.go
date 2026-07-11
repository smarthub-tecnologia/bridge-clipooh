package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/linkkotech/bridge/internal/models"
)

type TenantRepository interface {
	Create(ctx context.Context, t *models.Tenant) error
	CreateTx(ctx context.Context, tx pgx.Tx, t *models.Tenant) error
	UpdateTx(ctx context.Context, tx pgx.Tx, t *models.Tenant) error
	FindByID(ctx context.Context, id string) (*models.Tenant, error)
	FindByWorkspaceID(ctx context.Context, workspaceID string) (*models.Tenant, error)
	FindByChatwootAccountID(ctx context.Context, accountID int) (*models.Tenant, error)
	Update(ctx context.Context, t *models.Tenant) error
	Delete(ctx context.Context, id string) error
}

type tenantRepo struct {
	db *pgxpool.Pool
}

func NewTenantRepository(db *pgxpool.Pool) TenantRepository {
	return &tenantRepo{db: db}
}

const tenantSelectFields = `
	id, workspace_id, name, domain, status, chatwoot_account_id,
	chatwoot_account_name, chatwoot_api_token, evolution_base_url, evolution_default_instance,
	webhook_evolution_configured, webhook_chatwoot_configured, chatwoot_webhook_secret, metadata, created_at, updated_at`

func scanTenant(row interface{ Scan(...any) error }) (*models.Tenant, error) {
	var t models.Tenant
	err := row.Scan(
		&t.ID, &t.WorkspaceID, &t.Name, &t.Domain, &t.Status, &t.ChatwootAccountID,
		&t.ChatwootAccountName, &t.ChatwootAPIToken, &t.EvolutionBaseURL, &t.EvolutionDefaultInstance,
		&t.WebhookEvolutionConfigured, &t.WebhookChatwootConfigured, &t.ChatwootWebhookSecret, &t.Metadata, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *tenantRepo) Create(ctx context.Context, t *models.Tenant) error {
	query := `
		INSERT INTO tenants (
			id, workspace_id, name, domain, external_id, status, chatwoot_account_id,
			chatwoot_account_name, chatwoot_api_token, evolution_base_url, evolution_default_instance,
			webhook_evolution_configured, webhook_chatwoot_configured, chatwoot_webhook_secret, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		) RETURNING created_at, updated_at
	`
	return r.db.QueryRow(ctx, query,
		t.ID, t.WorkspaceID, t.Name, t.Domain, t.ExternalID, t.Status, t.ChatwootAccountID,
		t.ChatwootAccountName, t.ChatwootAPIToken, t.EvolutionBaseURL, t.EvolutionDefaultInstance,
		t.WebhookEvolutionConfigured, t.WebhookChatwootConfigured, t.ChatwootWebhookSecret, t.Metadata,
	).Scan(&t.CreatedAt, &t.UpdatedAt)
}

func (r *tenantRepo) CreateTx(ctx context.Context, tx pgx.Tx, t *models.Tenant) error {
	query := `
		INSERT INTO tenants (
			id, workspace_id, name, domain, external_id, status, chatwoot_account_id,
			chatwoot_account_name, chatwoot_api_token, evolution_base_url, evolution_default_instance,
			webhook_evolution_configured, webhook_chatwoot_configured, chatwoot_webhook_secret, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		) RETURNING created_at, updated_at
	`
	return tx.QueryRow(ctx, query,
		t.ID, t.WorkspaceID, t.Name, t.Domain, t.ExternalID, t.Status, t.ChatwootAccountID,
		t.ChatwootAccountName, t.ChatwootAPIToken, t.EvolutionBaseURL, t.EvolutionDefaultInstance,
		t.WebhookEvolutionConfigured, t.WebhookChatwootConfigured, t.ChatwootWebhookSecret, t.Metadata,
	).Scan(&t.CreatedAt, &t.UpdatedAt)
}

func (r *tenantRepo) UpdateTx(ctx context.Context, tx pgx.Tx, t *models.Tenant) error {
	// workspace_id is immutable — not included in UPDATE
	query := `
		UPDATE tenants SET
			name = $1, domain = $2, status = $3,
			chatwoot_account_id = $4, chatwoot_account_name = $5,
			chatwoot_api_token = $6, evolution_default_instance = $7,
			chatwoot_webhook_secret = $8, evolution_base_url = $9,
			webhook_evolution_configured = $10, webhook_chatwoot_configured = $11,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $12
	`
	_, err := tx.Exec(ctx, query,
		t.Name, t.Domain, t.Status,
		t.ChatwootAccountID, t.ChatwootAccountName,
		t.ChatwootAPIToken, t.EvolutionDefaultInstance,
		t.ChatwootWebhookSecret, t.EvolutionBaseURL,
		t.WebhookEvolutionConfigured, t.WebhookChatwootConfigured,
		t.ID,
	)
	return err
}

func (r *tenantRepo) FindByID(ctx context.Context, id string) (*models.Tenant, error) {
	query := `SELECT` + tenantSelectFields + ` FROM tenants WHERE id = $1`
	return scanTenant(r.db.QueryRow(ctx, query, id))
}

func (r *tenantRepo) FindByWorkspaceID(ctx context.Context, workspaceID string) (*models.Tenant, error) {
	query := `SELECT` + tenantSelectFields + ` FROM tenants WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT 1`
	return scanTenant(r.db.QueryRow(ctx, query, workspaceID))
}

func (r *tenantRepo) FindByChatwootAccountID(ctx context.Context, accountID int) (*models.Tenant, error) {
	query := `SELECT` + tenantSelectFields + ` FROM tenants WHERE chatwoot_account_id = $1`
	return scanTenant(r.db.QueryRow(ctx, query, accountID))
}

func (r *tenantRepo) Update(ctx context.Context, t *models.Tenant) error {
	// workspace_id is immutable — not included in UPDATE
	query := `
		UPDATE tenants SET
			name = $1, domain = $2, status = $3,
			chatwoot_account_id = $4, chatwoot_account_name = $5,
			chatwoot_api_token = $6, evolution_default_instance = $7,
			chatwoot_webhook_secret = $8, evolution_base_url = $9,
			webhook_evolution_configured = $10, webhook_chatwoot_configured = $11,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $12
	`
	_, err := r.db.Exec(ctx, query,
		t.Name, t.Domain, t.Status,
		t.ChatwootAccountID, t.ChatwootAccountName,
		t.ChatwootAPIToken, t.EvolutionDefaultInstance,
		t.ChatwootWebhookSecret, t.EvolutionBaseURL,
		t.WebhookEvolutionConfigured, t.WebhookChatwootConfigured,
		t.ID,
	)
	return err
}

func (r *tenantRepo) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM tenants WHERE id = $1`
	_, err := r.db.Exec(ctx, query, id)
	return err
}
