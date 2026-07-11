package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InboxRepository struct {
	pool *pgxpool.Pool
}

func NewInboxRepository(pool *pgxpool.Pool) *InboxRepository {
	return &InboxRepository{pool: pool}
}

func (r *InboxRepository) GetDefaultInboxID(ctx context.Context, tenantID string) (int, error) {
	var inboxID int
	err := r.pool.QueryRow(ctx, "SELECT inbox_id FROM chatwoot_inboxes WHERE tenant_id = $1 LIMIT 1", tenantID).Scan(&inboxID)
	return inboxID, err
}

func (r *InboxRepository) SaveInbox(ctx context.Context, tenantID string, chatwootAccountID int, inboxID int, inboxName string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO chatwoot_inboxes (tenant_id, chatwoot_account_id, inbox_id, inbox_name, inbox_type, webhook_configured)
		 VALUES ($1, $2, $3, $4, 'api', true)
		 ON CONFLICT (chatwoot_account_id, inbox_id) DO UPDATE SET
		 	tenant_id = EXCLUDED.tenant_id,
		 	inbox_name = EXCLUDED.inbox_name,
		 	webhook_configured = true`,
		tenantID, chatwootAccountID, inboxID, inboxName,
	)
	return err
}

func (r *InboxRepository) SaveInboxTx(ctx context.Context, tx pgx.Tx, tenantID string, chatwootAccountID int, inboxID int, inboxName string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO chatwoot_inboxes (tenant_id, chatwoot_account_id, inbox_id, inbox_name, inbox_type, webhook_configured)
		 VALUES ($1, $2, $3, $4, 'api', true)
		 ON CONFLICT (chatwoot_account_id, inbox_id) DO UPDATE SET
		 	tenant_id = EXCLUDED.tenant_id,
		 	inbox_name = EXCLUDED.inbox_name,
		 	webhook_configured = true`,
		tenantID, chatwootAccountID, inboxID, inboxName,
	)
	return err
}
