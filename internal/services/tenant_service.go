package services

import (
	"context"

	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/repository"
)

type TenantService interface {
	CreateTenant(ctx context.Context, t *models.Tenant) error
	GetTenant(ctx context.Context, id string) (*models.Tenant, error)
	GetTenantByChatwootAccount(ctx context.Context, accountID int) (*models.Tenant, error)
	DeleteTenant(ctx context.Context, id string) error
}

type tenantService struct {
	repo repository.TenantRepository
}

func NewTenantService(repo repository.TenantRepository) TenantService {
	return &tenantService{repo: repo}
}

func (s *tenantService) CreateTenant(ctx context.Context, t *models.Tenant) error {
	return s.repo.Create(ctx, t)
}

func (s *tenantService) GetTenant(ctx context.Context, id string) (*models.Tenant, error) {
	return s.repo.FindByID(ctx, id)
}

func (s *tenantService) GetTenantByChatwootAccount(ctx context.Context, accountID int) (*models.Tenant, error) {
	return s.repo.FindByChatwootAccountID(ctx, accountID)
}

func (s *tenantService) DeleteTenant(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}
