package repository

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type AddonEventRepository interface {
	InsertEvent(ctx context.Context, workspaceID, resourceID, addonType, eventType, triggeredBy string, metadata map[string]interface{}) error
}

type addonEventRepo struct {
	pool *pgxpool.Pool
}

func NewAddonEventRepository(pool *pgxpool.Pool) AddonEventRepository {
	return &addonEventRepo{pool: pool}
}

func (r *addonEventRepo) InsertEvent(ctx context.Context, workspaceID, resourceID, addonType, eventType, triggeredBy string, metadata map[string]interface{}) error {
	if r.pool == nil {
		zap.L().Debug("directus pool is nil, skipping addon_events insert")
		return nil
	}

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}

	query := `
		INSERT INTO addon_events (
			id, workspace_id, resource_id, addon_type, event_type, triggered_by, metadata, date_created
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`
	_, err = r.pool.Exec(ctx, query, uuid.New().String(), workspaceID, resourceID, addonType, eventType, triggeredBy, metadataJSON)
	if err != nil {
		zap.L().Error("failed to insert addon_event", zap.Error(err), zap.String("resource_id", resourceID))
	}
	return err
}
