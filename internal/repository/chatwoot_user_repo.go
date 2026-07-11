package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChatwootUserRepository cacheia email -> chatwoot_user_id — ver comentário
// em migrations/017_create_chatwoot_users.up.sql para o motivo.
type ChatwootUserRepository struct {
	pool *pgxpool.Pool
}

func NewChatwootUserRepository(pool *pgxpool.Pool) *ChatwootUserRepository {
	return &ChatwootUserRepository{pool: pool}
}

// Upsert grava/atualiza o id do usuário Chatwoot conhecido para este e-mail.
func (r *ChatwootUserRepository) Upsert(ctx context.Context, email string, chatwootUserID int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO chatwoot_users (email, chatwoot_user_id)
		VALUES ($1, $2)
		ON CONFLICT (email) DO UPDATE
		SET chatwoot_user_id = EXCLUDED.chatwoot_user_id, updated_at = NOW()
	`, strings.ToLower(email), chatwootUserID)
	return err
}

// GetByEmail retorna (id, true, nil) se já vimos esse e-mail antes,
// (0, false, nil) se não, ou (0, false, err) em falha de banco.
func (r *ChatwootUserRepository) GetByEmail(ctx context.Context, email string) (int, bool, error) {
	var id int
	err := r.pool.QueryRow(ctx, `
		SELECT chatwoot_user_id FROM chatwoot_users WHERE email = $1
	`, strings.ToLower(email)).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
