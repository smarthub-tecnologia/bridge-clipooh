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

// GetDefaultInboxID retorna a inbox WhatsApp ("api") da conta Chatwoot única
// da Cartão Pro. Não há mais escopo por tenant — só existe uma conta.
func (r *InboxRepository) GetDefaultInboxID(ctx context.Context) (int, error) {
	var inboxID int
	err := r.pool.QueryRow(ctx, "SELECT inbox_id FROM chatwoot_inboxes WHERE inbox_type = 'api' LIMIT 1").Scan(&inboxID)
	return inboxID, err
}

func (r *InboxRepository) SaveInbox(ctx context.Context, chatwootAccountID int, inboxID int, inboxName string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO chatwoot_inboxes (chatwoot_account_id, inbox_id, inbox_name, inbox_type, webhook_configured)
		 VALUES ($1, $2, $3, 'api', true)
		 ON CONFLICT (chatwoot_account_id, inbox_id) DO UPDATE SET
		 	inbox_name = EXCLUDED.inbox_name,
		 	webhook_configured = true`,
		chatwootAccountID, inboxID, inboxName,
	)
	return err
}

func (r *InboxRepository) SaveInboxTx(ctx context.Context, tx pgx.Tx, chatwootAccountID int, inboxID int, inboxName string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO chatwoot_inboxes (chatwoot_account_id, inbox_id, inbox_name, inbox_type, webhook_configured)
		 VALUES ($1, $2, $3, 'api', true)
		 ON CONFLICT (chatwoot_account_id, inbox_id) DO UPDATE SET
		 	inbox_name = EXCLUDED.inbox_name,
		 	webhook_configured = true`,
		chatwootAccountID, inboxID, inboxName,
	)
	return err
}
