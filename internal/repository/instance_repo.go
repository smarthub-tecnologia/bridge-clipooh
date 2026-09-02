package repository

import (
	"context"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/linkkotech/bridge/internal/models"
)

type InstanceRepository interface {
	Create(ctx context.Context, i *models.EvolutionInstance) error
	CreateTx(ctx context.Context, tx pgx.Tx, i *models.EvolutionInstance) error
	FindByInstanceID(ctx context.Context, instanceID string) (*models.EvolutionInstance, error)
	FindByInstanceName(ctx context.Context, instanceName string) (*models.EvolutionInstance, error)
	FindDefault(ctx context.Context) (*models.EvolutionInstance, error)
	FindAll(ctx context.Context) ([]models.EvolutionInstance, error)
	UpdateStatus(ctx context.Context, instanceID, status string) error
	Update(ctx context.Context, i *models.EvolutionInstance) error
	GetConnectedAt(ctx context.Context, instanceName string) (*time.Time, error)
	UpdateConnectionState(ctx context.Context, instanceName, state string, phone *string) error
	LogoutInstance(ctx context.Context, instanceName string) error
	DeleteInstance(ctx context.Context, instanceName string) error
}

type instanceRepo struct {
	db *pgxpool.Pool
}

func NewInstanceRepository(db *pgxpool.Pool) InstanceRepository {
	return &instanceRepo{db: db}
}

const instanceSelectFields = `
	id, instance_id, instance_name, api_key, base_url, status,
	qr_code, phone_number, webhook_url, webhook_events,
	connected_at, disconnected_at, disconnect_reason,
	messages_sent, messages_received, config, metadata, created_at, updated_at`

func scanInstance(row interface{ Scan(...any) error }) (*models.EvolutionInstance, error) {
	var i models.EvolutionInstance
	err := row.Scan(
		&i.ID, &i.InstanceID, &i.InstanceName, &i.APIKey, &i.BaseURL, &i.Status,
		&i.QRCode, &i.PhoneNumber, &i.WebhookURL, &i.WebhookEvents,
		&i.ConnectedAt, &i.DisconnectedAt, &i.DisconnectReason,
		&i.MessagesSent, &i.MessagesReceived, &i.Config, &i.Metadata, &i.CreatedAt, &i.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func (r *instanceRepo) Create(ctx context.Context, i *models.EvolutionInstance) error {
	query := `
		INSERT INTO evolution_instances (
			instance_id, instance_name, api_key, base_url, status,
			qr_code, phone_number, webhook_url, webhook_events, config, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
		) RETURNING id, created_at, updated_at
	`
	return r.db.QueryRow(ctx, query,
		i.InstanceID, i.InstanceName, i.APIKey, i.BaseURL, i.Status,
		i.QRCode, i.PhoneNumber, i.WebhookURL, i.WebhookEvents, i.Config, i.Metadata,
	).Scan(&i.ID, &i.CreatedAt, &i.UpdatedAt)
}

func (r *instanceRepo) CreateTx(ctx context.Context, tx pgx.Tx, i *models.EvolutionInstance) error {
	query := `
		INSERT INTO evolution_instances (
			instance_id, instance_name, api_key, base_url, status,
			qr_code, phone_number, webhook_url, webhook_events, config, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
		) RETURNING id, created_at, updated_at
	`
	return tx.QueryRow(ctx, query,
		i.InstanceID, i.InstanceName, i.APIKey, i.BaseURL, i.Status,
		i.QRCode, i.PhoneNumber, i.WebhookURL, i.WebhookEvents, i.Config, i.Metadata,
	).Scan(&i.ID, &i.CreatedAt, &i.UpdatedAt)
}

func (r *instanceRepo) FindByInstanceID(ctx context.Context, instanceID string) (*models.EvolutionInstance, error) {
	query := `SELECT` + instanceSelectFields + ` FROM evolution_instances WHERE instance_id = $1`
	return scanInstance(r.db.QueryRow(ctx, query, instanceID))
}

func (r *instanceRepo) FindByInstanceName(ctx context.Context, instanceName string) (*models.EvolutionInstance, error) {
	query := `SELECT` + instanceSelectFields + ` FROM evolution_instances WHERE instance_name = $1`
	return scanInstance(r.db.QueryRow(ctx, query, instanceName))
}

// FindDefault retorna a instância a usar quando o caller não especifica uma
// explicitamente (ex: POST /notify/send sem "instance"). Tenta EVOLUTION_INSTANCE
// (nome fixo configurado via env var); se ausente ou não encontrada, cai para a
// instância mais antiga cadastrada — não há mais conceito de tenant para decidir isso.
func (r *instanceRepo) FindDefault(ctx context.Context) (*models.EvolutionInstance, error) {
	if name := os.Getenv("EVOLUTION_INSTANCE"); name != "" {
		if inst, err := r.FindByInstanceName(ctx, name); err == nil {
			return inst, nil
		}
	}
	query := `SELECT` + instanceSelectFields + ` FROM evolution_instances ORDER BY created_at ASC LIMIT 1`
	return scanInstance(r.db.QueryRow(ctx, query))
}

func (r *instanceRepo) Update(ctx context.Context, i *models.EvolutionInstance) error {
	query := `
		UPDATE evolution_instances SET
			instance_id = $1, instance_name = $2, api_key = $3,
			base_url = $4, status = $5, webhook_url = $6,
			updated_at = CURRENT_TIMESTAMP
		WHERE instance_id = $1 OR instance_name = $2
	`
	_, err := r.db.Exec(ctx, query,
		i.InstanceID, i.InstanceName, i.APIKey,
		i.BaseURL, i.Status, i.WebhookURL,
	)
	return err
}

func (r *instanceRepo) UpdateStatus(ctx context.Context, instanceID, status string) error {
	query := `UPDATE evolution_instances SET status = $1, updated_at = CURRENT_TIMESTAMP WHERE instance_id = $2`
	_, err := r.db.Exec(ctx, query, status, instanceID)
	return err
}

func (r *instanceRepo) FindAll(ctx context.Context) ([]models.EvolutionInstance, error) {
	rows, err := r.db.Query(ctx, "SELECT instance_name, status, phone_number FROM evolution_instances")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var instances []models.EvolutionInstance
	for rows.Next() {
		var inst models.EvolutionInstance
		var phone *string
		if err := rows.Scan(&inst.InstanceName, &inst.Status, &phone); err != nil {
			return nil, err
		}
		inst.PhoneNumber = phone
		instances = append(instances, inst)
	}
	return instances, nil
}

func (r *instanceRepo) GetConnectedAt(ctx context.Context, instanceName string) (*time.Time, error) {
	var connectedAt *time.Time
	err := r.db.QueryRow(ctx, "SELECT connected_at FROM evolution_instances WHERE instance_name = $1", instanceName).Scan(&connectedAt)
	if err != nil {
		return nil, err
	}
	if connectedAt == nil {
		now := time.Now()
		return &now, nil
	}
	return connectedAt, nil
}

func (r *instanceRepo) UpdateConnectionState(ctx context.Context, instanceName, state string, phone *string) error {
	if state == "open" {
		query := `
			UPDATE evolution_instances
			SET status = 'connected',
			    phone_number = $1,
			    connected_at = now(),
			    disconnected_at = NULL,
			    disconnect_reason = NULL,
			    updated_at = now()
			WHERE instance_name = $2`
		_, err := r.db.Exec(ctx, query, phone, instanceName)
		return err
	} else if state == "close" {
		query := `
			UPDATE evolution_instances
			SET status = 'disconnected',
			    disconnected_at = now(),
			    disconnect_reason = 'connection_closed',
			    updated_at = now()
			WHERE instance_name = $1`
		_, err := r.db.Exec(ctx, query, instanceName)
		return err
	} else if state == "connecting" {
		query := `UPDATE evolution_instances SET status = 'connecting', updated_at = now() WHERE instance_name = $1`
		_, err := r.db.Exec(ctx, query, instanceName)
		return err
	}
	return nil
}

func (r *instanceRepo) LogoutInstance(ctx context.Context, instanceName string) error {
	query := `
		UPDATE evolution_instances
		SET status = 'disconnected',
		    phone_number = NULL,
		    disconnected_at = now(),
		    disconnect_reason = 'logout',
		    updated_at = now()
		WHERE instance_name = $1`
	_, err := r.db.Exec(ctx, query, instanceName)
	return err
}

func (r *instanceRepo) DeleteInstance(ctx context.Context, instanceName string) error {
	query := `UPDATE evolution_instances SET status = 'deleted', updated_at = now() WHERE instance_name = $1`
	_, err := r.db.Exec(ctx, query, instanceName)
	return err
}
