package repository

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/linkkotech/bridge/internal/models"
)

type NotificationRepository struct {
	pool *pgxpool.Pool
}

func NewNotificationRepository(pool *pgxpool.Pool) *NotificationRepository {
	return &NotificationRepository{pool: pool}
}

// externalID, quando não vazio, é usado como o id da notificação em vez de um
// UUID gerado aqui — permite ao caller (Next.js) correlacionar a resposta
// assíncrona (callback) sem depender de um ID que só a Bridge conhece. É
// também o "event_id" usado por pkg/metaconfig.LookupEventIDByPhone pra
// correlacionar callbacks de entrega da Meta Cloud API.
func (r *NotificationRepository) Create(ctx context.Context, tenantID, idempotencyKey, to, msgType string, content json.RawMessage, externalID, instanceName string) (*models.NotificationResponse, error) {
	id := externalID
	if id == "" {
		id = uuid.New().String()
	}
	_, err := r.pool.Exec(ctx, `
        INSERT INTO notifications (id, tenant_id, idempotency_key, to_number, type, content, status, instance_name, created_at)
        VALUES ($1, NULLIF($2, '')::UUID, $3, $4, $5, $6, 'queued', $7, NOW())
    `, id, tenantID, idempotencyKey, to, msgType, content, instanceName)
	if err != nil {
		return nil, err
	}
	return &models.NotificationResponse{
		NotificationID: id,
		IdempotencyKey: idempotencyKey,
		Status:         "queued",
	}, nil
}

func (r *NotificationRepository) UpdateStatus(ctx context.Context, notificationID, status, evolutionMessageID, errorMsg string) error {
	_, err := r.pool.Exec(ctx, `
        UPDATE notifications SET status = $2, evolution_message_id = $3, error_message = $4,
            sent_at = CASE WHEN $2 = 'sent' THEN NOW() WHEN $2 = 'delivered' THEN NOW() ELSE sent_at END,
            delivered_at = CASE WHEN $2 = 'delivered' THEN NOW() ELSE delivered_at END
        WHERE id = $1
    `, notificationID, status, evolutionMessageID, errorMsg)
	return err
}

func (r *NotificationRepository) FindByIDOrKey(ctx context.Context, idOrKey string) (*models.NotificationStatusResponse, error) {
	var resp models.NotificationStatusResponse
	err := r.pool.QueryRow(ctx, `
        SELECT id, idempotency_key, status, to_number, sent_at, delivered_at, error_message
        FROM notifications WHERE id::text = $1 OR idempotency_key = $1
        ORDER BY created_at DESC LIMIT 1
    `, idOrKey).Scan(&resp.NotificationID, &resp.IdempotencyKey, &resp.Status, &resp.To, &resp.SentAt, &resp.DeliveredAt, &resp.Error)
	return &resp, err
}
