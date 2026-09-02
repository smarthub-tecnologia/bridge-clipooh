package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/middleware"
	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/observability"
	"github.com/linkkotech/bridge/internal/services"
)

type EvolutionHandler struct {
	bridgeService *services.BridgeService
	authService   *middleware.AuthService
	metrics       *observability.MetricsCollector
}

func NewEvolutionHandler(bridgeService *services.BridgeService, authService *middleware.AuthService) *EvolutionHandler {
	return &EvolutionHandler{
		bridgeService: bridgeService,
		authService:   authService,
	}
}

func (h *EvolutionHandler) SetMetrics(m *observability.MetricsCollector) {
	h.metrics = m
}

func (h *EvolutionHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := zap.L().With(zap.String("handler", "evolution_webhook"))

	// Log imediato de entrada — confirma que o payload bateu na porta
	logger.Info("RAW WEBHOOK RECEIVED",
		zap.String("uri", r.RequestURI),
		zap.Any("headers", r.Header),
		zap.String("method", r.Method),
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("instance", r.URL.Query().Get("instance")),
		zap.Bool("token_in_query", r.URL.Query().Get("token") != ""),
	)

	// Valida autenticação da Evolution
	if !h.authService.ValidateEvolutionHMAC(r) {
		if h.metrics != nil {
			h.metrics.Error401Count.Add(1)
		}
		logger.Error("evolution webhook rejected: invalid auth",
			zap.String("remote_addr", r.RemoteAddr),
			zap.Bool("apikey_present", r.Header.Get("apikey") != ""),
			zap.Bool("token_in_query", r.URL.Query().Get("token") != ""),
			zap.Bool("signature_present", r.Header.Get("X-Evolution-Signature") != ""),
			zap.String("uri", r.RequestURI),
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}

	// Lê o body raw para log e reutilização no decode
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error("failed to read request body", zap.Error(err))
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	logger.Info("🚨 [RAW_WEBHOOK_EVOLUTION] Payload recebido", zap.String("raw_body", string(rawBody)))
	r.Body = io.NopCloser(bytes.NewBuffer(rawBody))

	var webhook models.EvolutionWebhook
	if err := json.NewDecoder(r.Body).Decode(&webhook); err != nil {
		logger.Error("invalid json", zap.Error(err))
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Injeta instance do query param na struct do webhook — resolução é 100%
	// por instance_name agora, não há mais ?tenant= na URL.
	webhook.InstanceName = r.URL.Query().Get("instance")

	logger.Info("evolution webhook parsed",
		zap.String("event", webhook.Event),
		zap.String("instance", webhook.InstanceName),
	)

	// Processa o webhook
	if err := h.bridgeService.HandleEvolutionWebhook(ctx, webhook); err != nil {
		logger.Error("failed to process webhook", zap.Error(err))
		// Ainda retornamos 200 para a Evolution não reenviar, mas logamos o erro
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": err.Error()})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "processed"})
}
