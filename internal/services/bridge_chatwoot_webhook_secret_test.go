package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/linkkotech/bridge/internal/config"
	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/repository"
)

// chatwootSecretFakeRepo é um repo falso focado em FindByChatwootInboxID —
// embute a interface para satisfazer os demais métodos sem implementá-los.
type chatwootSecretFakeRepo struct {
	repository.InstanceRepository
	byInboxID map[int]*models.EvolutionInstance
}

func (f *chatwootSecretFakeRepo) FindByChatwootInboxID(ctx context.Context, inboxID int) (*models.EvolutionInstance, error) {
	if inst, ok := f.byInboxID[inboxID]; ok {
		return inst, nil
	}
	return nil, errors.New("no rows")
}

func secretPtr(s string) *string { return &s }

func signChatwootBody(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestValidateChatwootWebhookSecretPerInstance garante que duas inboxes
// diferentes, cada uma com seu próprio chatwoot_inbox_webhook_secret, validam
// corretamente ao mesmo tempo — sem precisar de uma env var global comum.
func TestValidateChatwootWebhookSecretPerInstance(t *testing.T) {
	repo := &chatwootSecretFakeRepo{
		byInboxID: map[int]*models.EvolutionInstance{
			1: {InstanceName: "assistente-ooh", ChatwootInboxWebhookSecret: secretPtr("secret-assistente")},
			2: {InstanceName: "comercial-ooh", ChatwootInboxWebhookSecret: secretPtr("secret-comercial")},
		},
	}
	b := NewBridgeService(repo, nil, nil, nil, config.ChatwootConfig{WebhookSecret: "global-fallback-secret"})

	cases := []struct {
		name    string
		inboxID int
		secret  string
	}{
		{"assistente ooh", 1, "secret-assistente"},
		{"comercial ooh", 2, "secret-comercial"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"account":{"id":1},"inbox":{"id":` + strconv.Itoa(tc.inboxID) + `},"event":"message_created"}`
			req := httptest.NewRequest(http.MethodPost, "/webhook/chatwoot", strings.NewReader(body))
			req.Header.Set("X-Chatwoot-Signature", signChatwootBody(tc.secret, body))

			if !b.ValidateChatwootWebhookSecret(req) {
				t.Fatalf("esperava HMAC match para inbox_id=%d com secret por-instância", tc.inboxID)
			}

			// Assinar com o secret da OUTRA inbox deve falhar — cada instância só
			// aceita o seu próprio secret.
			reqWrong := httptest.NewRequest(http.MethodPost, "/webhook/chatwoot", strings.NewReader(body))
			reqWrong.Header.Set("X-Chatwoot-Signature", signChatwootBody("wrong-secret", body))
			if b.ValidateChatwootWebhookSecret(reqWrong) {
				t.Fatalf("esperava HMAC mismatch para inbox_id=%d com secret incorreto", tc.inboxID)
			}
		})
	}
}

// TestValidateChatwootWebhookSecretFallsBackToGlobal garante compatibilidade
// com configs antigas: se nenhuma instância corresponde ao inbox_id do
// payload (ou o payload não traz inbox_id), a validação cai para
// CHATWOOT_WEBHOOK_SECRET.
func TestValidateChatwootWebhookSecretFallsBackToGlobal(t *testing.T) {
	repo := &chatwootSecretFakeRepo{byInboxID: map[int]*models.EvolutionInstance{}}
	b := NewBridgeService(repo, nil, nil, nil, config.ChatwootConfig{WebhookSecret: "global-fallback-secret"})

	body := `{"account":{"id":1},"inbox":{"id":999},"event":"message_created"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/chatwoot", strings.NewReader(body))
	req.Header.Set("X-Chatwoot-Signature", signChatwootBody("global-fallback-secret", body))

	if !b.ValidateChatwootWebhookSecret(req) {
		t.Fatal("esperava fallback para CHATWOOT_WEBHOOK_SECRET quando nenhuma instância corresponde ao inbox_id")
	}
}
