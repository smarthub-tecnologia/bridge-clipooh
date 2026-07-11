package handlers

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/middleware"
	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/observability"
	"github.com/linkkotech/bridge/internal/services"
)

type ChatwootHandler struct {
	bridgeService *services.BridgeService
	authService   *middleware.AuthService
	metrics       *observability.MetricsCollector
}

func NewChatwootHandler(bridge *services.BridgeService, auth *middleware.AuthService) *ChatwootHandler {
	return &ChatwootHandler{bridgeService: bridge, authService: auth}
}

func (h *ChatwootHandler) SetMetrics(m *observability.MetricsCollector) {
	h.metrics = m
}

func (h *ChatwootHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := zap.L().With(zap.String("handler", "chatwoot_webhook"))

	// Log de porta de entrada — primeira linha absoluta, antes de qualquer validação
	logger.Info("OUTBOUND WEBHOOK ATTEMPT",
		zap.String("uri", r.RequestURI),
		zap.String("method", r.Method),
		zap.String("remote_addr", r.RemoteAddr),
		zap.Bool("has_signature", r.Header.Get("X-Chatwoot-Signature") != ""),
		zap.Bool("has_tenant", r.URL.Query().Get("tenant") != ""),
	)

	// Lê tenant da query string
	tenantID := r.URL.Query().Get("tenant")
	if tenantID == "" {
		logger.Warn("missing tenant query param")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Valida segredo do webhook contra o segredo salvo no banco do tenant
	if !h.bridgeService.ValidateChatwootWebhookSecret(ctx, tenantID, r) {
		if h.metrics != nil {
			h.metrics.Error401Count.Add(1)
		}
		logger.Warn("invalid chatwoot token", zap.String("tenant", tenantID))
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var webhook models.ChatwootWebhook
	if err := json.NewDecoder(r.Body).Decode(&webhook); err != nil {
		logger.Error("invalid json", zap.Error(err))
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Rastreio de IDs — cruza tenant da query param com account_id real do payload
	logger.Info("DEBUG: Chatwoot Account ID Trace",
		zap.String("tenant_query_param", tenantID),
		zap.Int("payload_account_id", webhook.Account.ID),
		zap.Int("payload_conversation_id", webhook.Conversation.ID),
		zap.String("event", webhook.Event),
		zap.String("message_type", webhook.MessageType),
	)

	if err := h.bridgeService.HandleChatwootWebhook(ctx, webhook); err != nil {
		logger.Error("failed to process chatwoot webhook", zap.Error(err))
		// Retorna 200 para evitar reenvio, mas loga o erro
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": err.Error()})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "processed"})
}
