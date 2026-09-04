package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/config"
	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/repository"
)

// qrTimeoutFakeRepo é um repo falso focado no fluxo de QRTimeout: expõe apenas
// FindByInstanceName e UpdateConnectionState (embute a interface para satisfazer
// os demais métodos sem implementá-los).
type qrTimeoutFakeRepo struct {
	repository.InstanceRepository
	inst         *models.EvolutionInstance
	stateChanges []string
}

func (f *qrTimeoutFakeRepo) FindByInstanceName(ctx context.Context, instanceName string) (*models.EvolutionInstance, error) {
	if f.inst == nil {
		return nil, errors.New("no rows")
	}
	return f.inst, nil
}

func (f *qrTimeoutFakeRepo) UpdateConnectionState(ctx context.Context, instanceName, state string, phone *string) error {
	f.stateChanges = append(f.stateChanges, state)
	return nil
}

// TestHandleQRTimeoutSkipsWhenInstanceAlreadyConnected é o teste do bug de
// produção: um QRTimeout tardio NÃO pode marcar a instância como desconectada
// nem disparar auto-reconnect quando a sessão já está conectada (status
// "connected" com telefone preenchido).
func TestHandleQRTimeoutSkipsWhenInstanceAlreadyConnected(t *testing.T) {
	phone := "+5522988010114"
	tok := "instance-token"
	repo := &qrTimeoutFakeRepo{
		inst: &models.EvolutionInstance{
			InstanceName: "inst-1",
			Status:       "connected",
			PhoneNumber:  &phone,
			APIKey:       &tok,
		},
	}
	b := NewBridgeService(repo, nil, nil, nil, config.ChatwootConfig{})

	wh := models.EvolutionWebhook{
		Event:        "QRTimeout",
		InstanceName: "inst-1",
		Data:         json.RawMessage(`{"qrcount":5,"maxCount":5,"forceLogout":true}`),
	}

	if err := b.handleQRTimeout(context.Background(), wh, zap.NewNop()); err != nil {
		t.Fatalf("handleQRTimeout retornou erro inesperado: %v", err)
	}

	if len(repo.stateChanges) != 0 {
		t.Fatalf("QRTimeout tardio alterou o estado de uma instância conectada: %v", repo.stateChanges)
	}
}

// TestHandleQRTimeoutStatusConnectedWithoutPhoneIsNotActive garante que status
// "connected" SEM telefone não é tratado como sessão ativa (evita regressão de
// comparar apenas a string de status).
func TestHandleQRTimeoutStatusConnectedWithoutPhoneIsNotActive(t *testing.T) {
	tok := "instance-token"
	repo := &qrTimeoutFakeRepo{
		inst: &models.EvolutionInstance{
			InstanceName: "inst-2",
			Status:       "connected",
			PhoneNumber:  nil,
			APIKey:       &tok,
		},
	}
	b := NewBridgeService(repo, nil, nil, nil, config.ChatwootConfig{})

	// qrcount < maxCount: sem auto-reconnect (para não disparar goroutine no teste).
	wh := models.EvolutionWebhook{
		Event:        "QRTimeout",
		InstanceName: "inst-2",
		Data:         json.RawMessage(`{"qrcount":1,"maxCount":5,"forceLogout":false}`),
	}

	if err := b.handleQRTimeout(context.Background(), wh, zap.NewNop()); err != nil {
		t.Fatalf("handleQRTimeout retornou erro inesperado: %v", err)
	}

	if len(repo.stateChanges) != 1 || repo.stateChanges[0] != "close" {
		t.Fatalf("esperava transição para disconnected, stateChanges=%v", repo.stateChanges)
	}
}
