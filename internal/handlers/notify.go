package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/middleware"
	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/services"
)

type NotifyHandler struct {
	notificationService *services.NotificationService
	authService         *middleware.AuthService
	cacheService        *services.CacheService
	adminService        *services.AdminService
}

func NewNotifyHandler(notificationService *services.NotificationService, authService *middleware.AuthService) *NotifyHandler {
	return &NotifyHandler{
		notificationService: notificationService,
		authService:         authService,
	}
}

func (h *NotifyHandler) SetCacheService(cacheService *services.CacheService) {
	h.cacheService = cacheService
}

func (h *NotifyHandler) SetAdminService(adminService *services.AdminService) {
	h.adminService = adminService
}

// SendMessage endpoint POST /api/v1/notify/send
func (h *NotifyHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := zap.L().With(zap.String("handler", "notify_send"))

	// Redundancy in case middleware is missing
	if !h.authService.ValidateBridgeAPIKey(r) {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key", nil)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Failed to read request body", nil)
		return
	}

	// Route to Meta Cloud API provider if requested.
	var probe struct {
		Provider string `json:"provider"`
	}
	_ = json.Unmarshal(body, &probe)
	if probe.Provider == "meta" {
		var req models.NotifyRequest
		if err := json.Unmarshal(body, &req); err != nil {
			respondError(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid request body", nil)
			return
		}
		h.handleMetaSend(w, r, &req)
		return
	}

	// Meta path returns early above — Evolution policy engine
	// (warmup, cooldown, rate limiting) is intentionally not applied
	// to Meta Cloud API sends. Meta manages its own rate limits.

	// Existing Evolution flow — unchanged behaviour.
	var req models.SendMessageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid request body", nil)
		return
	}

	idempotencyKey := r.Header.Get("X-Idempotency-Key")
	if idempotencyKey == "" {
		logger.Debug("no idempotency key provided")
	}

	resp, err := h.notificationService.SendMessage(ctx, req, idempotencyKey)
	if err != nil {
		logger.Error("failed to send message", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "SEND_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusAccepted, resp)
}

// SendTemplate endpoint POST /api/v1/notify/template
func (h *NotifyHandler) SendTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := zap.L().With(zap.String("handler", "notify_template"))

	if !h.authService.ValidateBridgeAPIKey(r) {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key", nil)
		return
	}

	var req models.SendTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid request body", nil)
		return
	}

	idempotencyKey := r.Header.Get("X-Idempotency-Key")
	resp, err := h.notificationService.SendTemplate(ctx, req, idempotencyKey)
	if err != nil {
		logger.Error("failed to send template", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "TEMPLATE_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusAccepted, resp)
}

// GetStatus endpoint GET /api/v1/notify/status/{id}
func (h *NotifyHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	if !h.authService.ValidateBridgeAPIKey(r) {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key", nil)
		return
	}

	status, err := h.notificationService.GetStatus(ctx, id)
	if err != nil {
		respondError(w, http.StatusNotFound, "NOT_FOUND", "Notification not found", nil)
		return
	}
	respondJSON(w, http.StatusOK, status)
}

// ListInstances endpoint GET /api/v1/instances
func (h *NotifyHandler) ListInstances(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !h.authService.ValidateBridgeAPIKey(r) {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key", nil)
		return
	}

	instances, err := h.notificationService.ListInstances(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, instances)
}

// GetQRCode endpoint GET /api/v1/instances/{instanceName}/qr
func (h *NotifyHandler) GetQRCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	instanceName := chi.URLParam(r, "instanceName")

	if !h.authService.ValidateBridgeAPIKey(r) {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key", nil)
		return
	}

	if h.cacheService == nil {
		respondError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Cache service not configured", nil)
		return
	}

	base64, ttl, err := h.cacheService.GetQRCode(ctx, instanceName)
	if err != nil || base64 == "" {
		respondError(w, http.StatusNotFound, "NOT_FOUND", "qr_expired_or_not_ready", nil)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"qr_base64":  base64,
		"expires_in": int(ttl.Seconds()),
	})
}

// TriggerConnect POST /api/v1/instances/{instanceName}/connect — endpoint
// canônico único de reconexão de instância (reconfigura webhook + gera QR).
// Body opcional {"token": "novo-token"} troca o token da instância antes de
// reconectar (capacidade que antes só existia em POST /admin/tenants/{id}/connect,
// removida junto com o conceito de tenant).
// Triggers QR code generation by reconnecting the Evolution GO instance.
// Polls Evolution GO GET /instance/qr directly (up to 10s) — no webhook dependency.
// Returns { status: "ready", qr_base64: "..." } if QR is available, or { status: "connecting" }.
func (h *NotifyHandler) TriggerConnect(w http.ResponseWriter, r *http.Request) {
	instanceName := chi.URLParam(r, "instanceName")

	if !h.authService.ValidateBridgeAPIKey(r) {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key", nil)
		return
	}

	if h.adminService == nil {
		respondError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Admin service not configured", nil)
		return
	}

	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body é opcional — ignoramos erro de decodificação

	qrBase64, err := h.adminService.ReconnectByInstanceName(r.Context(), instanceName, body.Token)
	if err != nil {
		zap.L().Error("failed to trigger QR code generation",
			zap.String("instance", instanceName),
			zap.Error(err),
		)
		respondError(w, http.StatusInternalServerError, "CONNECT_FAILED", err.Error(), nil)
		return
	}

	if qrBase64 == "ALREADY_CONNECTED" {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"status": "connected",
		})
		return
	}

	if qrBase64 != "" {
		// Cache in Redis so subsequent polls via GET /qr also work
		if h.cacheService != nil {
			_ = h.cacheService.SaveQRCode(r.Context(), instanceName, qrBase64, 60*time.Second)
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"status":     "ready",
			"qr_base64":  qrBase64,
			"expires_in": 55,
		})
		return
	}

	// QR not yet available — client should poll GET /qr (webhook may still deliver it)
	respondJSON(w, http.StatusOK, map[string]string{"status": "connecting"})
}

// LogoutInstance DELETE /api/v1/instances/{instanceName}/logout
// Força logout da instância na Evolution, limpa cache Redis e marca banco como disconnected.
func (h *NotifyHandler) LogoutInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	instanceName := chi.URLParam(r, "instanceName")

	if !h.authService.ValidateBridgeAPIKey(r) {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key", nil)
		return
	}

	if h.adminService == nil {
		respondError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Admin service not configured", nil)
		return
	}

	if err := h.adminService.ForceLogoutInstance(ctx, instanceName); err != nil {
		zap.L().Error("failed to force logout instance",
			zap.String("instance", instanceName),
			zap.Error(err),
		)
		respondError(w, http.StatusInternalServerError, "LOGOUT_FAILED", err.Error(), nil)
		return
	}

	if h.cacheService != nil {
		h.cacheService.DeleteQRCode(ctx, instanceName)
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

// RecreateInstance POST /api/v1/instances/{instanceName}/recreate
// Deleta a instância corrompida na Evolution GO e a recria com o mesmo nome e token.
// Usar quando a instância está em estado "client disconnected" e não responde a logout/reconnect.
func (h *NotifyHandler) RecreateInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	instanceName := chi.URLParam(r, "instanceName")

	if !h.authService.ValidateBridgeAPIKey(r) {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key", nil)
		return
	}

	if h.adminService == nil {
		respondError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Admin service not configured", nil)
		return
	}

	if err := h.adminService.RecreateInstance(ctx, instanceName); err != nil {
		zap.L().Error("failed to recreate instance",
			zap.String("instance", instanceName),
			zap.Error(err),
		)
		respondError(w, http.StatusInternalServerError, "RECREATE_FAILED", err.Error(), nil)
		return
	}

	if h.cacheService != nil {
		h.cacheService.DeleteQRCode(ctx, instanceName)
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "recreated"})
}
