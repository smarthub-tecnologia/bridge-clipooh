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

// ReplaceDefaultInbox aponta a tabela chatwoot_inboxes para UMA inbox única:
// a inbox Channel::Api do bridge. Antes de inserir, remove as demais linhas da
// conta — o histórico de fallbacks podia gravar linhas de inboxes de outros
// canais (ex.: Channel::Whatsapp) rotuladas como 'api', e o LIMIT 1 do
// GetDefaultInboxID podia escolher a linha errada (o que faz o Chatwoot
// rejeitar a criação de conversa com 422 "invalid source id for whatsapp inbox").
func (r *InboxRepository) ReplaceDefaultInbox(ctx context.Context, chatwootAccountID int, inboxID int, inboxName string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM chatwoot_inboxes WHERE chatwoot_account_id = $1`, chatwootAccountID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO chatwoot_inboxes (chatwoot_account_id, inbox_id, inbox_name, inbox_type, webhook_configured)
		 VALUES ($1, $2, $3, 'api', true)`,
		chatwootAccountID, inboxID, inboxName,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
