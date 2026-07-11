package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/linkkotech/bridge/internal/config"
	"go.uber.org/zap"
)

type CallbackPayload struct {
	// EventType distinguishes message-delivery callbacks from connection events.
	// Values: "notification.sent" | "notification.blocked_rate" | "notification.blocked_plan" | "connection.open" | "connection.close" | "instance.deleted"
	EventType    string    `json:"event_type,omitempty"`
	LogID        string    `json:"event_id"`
	TenantID     string    `json:"tenant_id"`
	WorkspaceID  string    `json:"workspace_id,omitempty"`
	InstanceName string    `json:"instance_name"`
	Status       string    `json:"status"`           // sent | failed | blocked_* | connected | disconnected | deleted
	Reason       string    `json:"reason,omitempty"` // connected | connection_closed | logout | instance_deleted
	PhoneNumber  string    `json:"phone_number,omitempty"`
	MsgID        string    `json:"evolution_msg_id,omitempty"`
	Error        string    `json:"error,omitempty"`
	SentAt       time.Time `json:"sent_at"`

	// Campos usados só no formato "event"-based (message.received) — mesmo
	// shape que meta_webhook.go já usa pro provider Meta (metaMessageReceivedCallback),
	// agora reaproveitado aqui pro provider Evolution via este mesmo Notify()
	// já testado em produção pros demais event_type.
	Event    string               `json:"event,omitempty"`
	Provider string               `json:"provider,omitempty"`
	Reply    *CallbackReplyDetail `json:"reply,omitempty"`
}

// CallbackReplyDetail — corpo da resposta do contato, usado só quando Event == "message.received".
type CallbackReplyDetail struct {
	From        string `json:"from"`
	Text        string `json:"text"`
	ReplyAt     string `json:"reply_at"`
	MessageType string `json:"message_type,omitempty"`
}

type CallbackService struct {
	config     config.CallbacksConfig
	httpClient *http.Client
}

func NewCallbackService(cfg config.CallbacksConfig) *CallbackService {
	return &CallbackService{
		config:     cfg,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

func (s *CallbackService) Notify(ctx context.Context, payload CallbackPayload) error {
	if !s.config.Enabled || s.config.DirectusWebhookURL == "" {
		return nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal callback payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.config.DirectusWebhookURL, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	bodyPreview := string(body)
	if len(bodyPreview) > 100 {
		bodyPreview = bodyPreview[:100]
	}

	if s.config.DirectusWebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(s.config.DirectusWebhookSecret))
		mac.Write(body)
		signature := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Bridge-Signature", signature)
	}

	zap.L().Info("🚨 [BRIDGE_OUTGOING_WEBHOOK] Atirando callback!",
		zap.String("url", s.config.DirectusWebhookURL),
		zap.String("payload", string(body)),
	)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		zap.L().Error("⛔ [BRIDGE_OUTGOING_ERROR] Falha ao enviar callback",
			zap.String("url", s.config.DirectusWebhookURL),
			zap.String("erro", err.Error()),
		)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		zap.L().Error("⛔ [BRIDGE_OUTGOING_ERROR] Resposta de erro do destino",
			zap.String("url", s.config.DirectusWebhookURL),
			zap.Int("status", resp.StatusCode),
		)
		return fmt.Errorf("callback returned status %d", resp.StatusCode)
	}

	zap.L().Info("✅ [BRIDGE_OUTGOING_RESULT] Resposta do destino",
		zap.String("url", s.config.DirectusWebhookURL),
		zap.Int("status", resp.StatusCode),
	)

	return nil
}
