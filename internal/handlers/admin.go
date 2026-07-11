package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/middleware"
	"github.com/linkkotech/bridge/internal/services"
)

type AdminHandler struct {
	adminService *services.AdminService
	authService  *middleware.AuthService
}

func NewAdminHandler(adminService *services.AdminService, authService *middleware.AuthService) *AdminHandler {
	return &AdminHandler{adminService: adminService, authService: authService}
}

func (h *AdminHandler) CreateTenant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := zap.L().With(zap.String("handler", "create_tenant"))

	var req services.CreateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid request body", nil)
		return
	}

	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "MISSING_FIELDS", "name is required", nil)
		return
	}
	if !req.EvolutionOnly && (req.AdminEmail == "" || req.AdminPassword == "") {
		respondError(w, http.StatusBadRequest, "MISSING_FIELDS", "admin_email and admin_password are required when evolution_only is false", nil)
		return
	}

	resp, err := h.adminService.CreateTenant(ctx, req)
	if err != nil {
		logger.Error("failed to create tenant", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "PROVISIONING_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusCreated, resp)
}

func (h *AdminHandler) GetTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")
	tenant, err := h.adminService.GetTenant(r.Context(), tenantID)
	if err != nil {
		respondError(w, http.StatusNotFound, "TENANT_NOT_FOUND", "Tenant not found", nil)
		return
	}
	respondJSON(w, http.StatusOK, tenant)
}

func (h *AdminHandler) DeleteTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")
	if err := h.adminService.DeleteTenant(r.Context(), tenantID); err != nil {
		respondError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *AdminHandler) ConnectTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")

	var body struct {
		Token string `json:"token"`
	}
	// body é opcional — ignoramos erro de decodificação
	_ = json.NewDecoder(r.Body).Decode(&body)

	if err := h.adminService.ReconnectInstance(r.Context(), tenantID, body.Token); err != nil {
		zap.L().Error("failed to reconnect instance", zap.String("tenant_id", tenantID), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "CONNECT_FAILED", err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "connected"})
}

func (h *AdminHandler) SetChatwootWebhookSecret(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")

	var body struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Secret == "" {
		respondError(w, http.StatusBadRequest, "INVALID_BODY", "secret is required", nil)
		return
	}

	tenant, err := h.adminService.GetTenant(r.Context(), tenantID)
	if err != nil {
		respondError(w, http.StatusNotFound, "NOT_FOUND", "tenant not found", nil)
		return
	}
	tenant.ChatwootWebhookSecret = &body.Secret
	if err := h.adminService.UpdateTenant(r.Context(), tenant); err != nil {
		respondError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// CreateWidgetInbox cria uma inbox tipo Website (widget público) na mesma
// conta Chatwoot do tenant — usada pelo addon "Widget de Chat do Perfil
// Digital". Não persiste nada localmente: só fala com o Chatwoot e devolve
// os dados pro Next.js gravar em workspace_chat_inboxes.
func (h *AdminHandler) CreateWidgetInbox(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")

	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // name é opcional — usa default se ausente/corpo vazio

	resp, err := h.adminService.CreateWidgetInbox(r.Context(), tenantID, body.Name)
	if err != nil {
		zap.L().Error("failed to create widget inbox", zap.String("tenant_id", tenantID), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "WIDGET_INBOX_FAILED", err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusCreated, resp)
}

// SyncWidgetWebhook migra o webhook de conta do widget pra incluir eventos
// novos (ex.: typing) em tenants que provisionaram o widget antes desses
// eventos existirem em widgetWebhookSubscriptions. Não recria a inbox —
// diferente de CreateWidgetInbox, é seguro chamar em tenants já ativos.
func (h *AdminHandler) SyncWidgetWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")

	resp, err := h.adminService.SyncWidgetWebhook(r.Context(), tenantID)
	if err != nil {
		zap.L().Error("failed to sync widget webhook", zap.String("tenant_id", tenantID), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "WIDGET_WEBHOOK_SYNC_FAILED", err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, resp)
}

// CreateAgent vincula um profile do workspace como agente numa conta Chatwoot
// já provisionada — usado pelo combobox da aba "Usuários" do addon combo.
// Nunca devolve senha (gerada e descartada internamente); dispara o e-mail
// nativo de definição de senha do Chatwoot.
func (h *AdminHandler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")
	logger := zap.L().With(zap.String("handler", "create_agent"), zap.String("tenant_id", tenantID))

	var req services.CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid request body", nil)
		return
	}
	if req.Email == "" || req.Name == "" {
		respondError(w, http.StatusBadRequest, "MISSING_FIELDS", "email and name are required", nil)
		return
	}

	resp, err := h.adminService.CreateAgent(r.Context(), tenantID, req)
	if err != nil {
		logger.Error("failed to create agent", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "CREATE_AGENT_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusCreated, resp)
}

// RemoveAgent desvincula um agente de uma conta Chatwoot já provisionada.
func (h *AdminHandler) RemoveAgent(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")
	agentIDStr := chi.URLParam(r, "agentId")
	logger := zap.L().With(zap.String("handler", "remove_agent"), zap.String("tenant_id", tenantID))

	agentID, err := strconv.Atoi(agentIDStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_AGENT_ID", "agentId must be numeric", nil)
		return
	}

	if err := h.adminService.RemoveAgent(r.Context(), tenantID, agentID); err != nil {
		logger.Error("failed to remove agent", zap.Int("agent_id", agentID), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "REMOVE_AGENT_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (h *AdminHandler) SyncChatwootSecret(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "id")

	secretLen, err := h.adminService.SyncChatwootSecret(r.Context(), tenantID)
	if err != nil {
		zap.L().Error("sync_chatwoot_secret failed", zap.String("tenant_id", tenantID), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "SYNC_FAILED", err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"status": "synced", "secret_len": secretLen})
}

// Helpers
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, code, message string, details map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   message,
		"code":    code,
		"details": details,
	})
}
