package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/middleware"
	"github.com/linkkotech/bridge/internal/services"
)

// AdminHandler expõe operações administrativas do bridge single-tenant da
// Cartão Pro: provisionamento de instâncias Evolution GO e manutenção da
// única conta Chatwoot (widget, agentes). Não há mais rotas de tenant nem de
// rotação de secret — a conta Chatwoot é fixa (CHATWOOT_ACCOUNT_ID/API_TOKEN),
// e CHATWOOT_WEBHOOK_SECRET é rotacionado via env var + redeploy (ver
// comentário em internal/config/config.go).
type AdminHandler struct {
	adminService *services.AdminService
	authService  *middleware.AuthService
}

func NewAdminHandler(adminService *services.AdminService, authService *middleware.AuthService) *AdminHandler {
	return &AdminHandler{adminService: adminService, authService: authService}
}

// CreateInstance POST /api/v1/admin/instances — provisiona uma nova linha
// WhatsApp (instância Evolution GO) na conta Cartão Pro já existente.
func (h *AdminHandler) CreateInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := zap.L().With(zap.String("handler", "create_instance"))

	var req services.CreateInstanceRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // body é opcional (instance_name pode ser omitido)

	resp, err := h.adminService.CreateInstance(ctx, req)
	if err != nil {
		logger.Error("failed to create instance", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "PROVISIONING_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusCreated, resp)
}

// GetInstanceChatwootInbox GET /api/v1/admin/instances/{instanceName}/inbox —
// devolve o vínculo atual da instância com a inbox Chatwoot.
func (h *AdminHandler) GetInstanceChatwootInbox(w http.ResponseWriter, r *http.Request) {
	instanceName := chi.URLParam(r, "instanceName")

	binding, err := h.adminService.GetInstanceChatwootInbox(r.Context(), instanceName)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") || strings.Contains(err.Error(), "not found") {
			respondError(w, http.StatusNotFound, "INSTANCE_NOT_FOUND", err.Error(), nil)
			return
		}
		zap.L().Error("failed to get instance chatwoot inbox", zap.String("instance", instanceName), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "GET_INBOX_BINDING_FAILED", err.Error(), nil)
		return
	}

	if binding == nil {
		// Instância existe mas ainda não tem inbox vinculada.
		respondJSON(w, http.StatusOK, services.ChatwootInboxBinding{})
		return
	}
	respondJSON(w, http.StatusOK, binding)
}

// SetInstanceChatwootInbox PUT /api/v1/admin/instances/{instanceName}/inbox —
// vincula (ou substitui) a inbox Chatwoot da instância. Corpo é o mesmo objeto
// usado no form de criação: { inbox_id, inbox_name?, webhook_secret?, inbox_identifier? }.
func (h *AdminHandler) SetInstanceChatwootInbox(w http.ResponseWriter, r *http.Request) {
	instanceName := chi.URLParam(r, "instanceName")

	var binding services.ChatwootInboxBinding
	if err := json.NewDecoder(r.Body).Decode(&binding); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid request body", nil)
		return
	}

	saved, err := h.adminService.SetInstanceChatwootInbox(r.Context(), instanceName, binding)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			respondError(w, http.StatusNotFound, "INSTANCE_NOT_FOUND", err.Error(), nil)
			return
		}
		zap.L().Error("failed to set instance chatwoot inbox", zap.String("instance", instanceName), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "SET_INBOX_BINDING_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusOK, saved)
}

// RemoveInstanceChatwootInbox DELETE /api/v1/admin/instances/{instanceName}/inbox —
// remove o vínculo da instância com a inbox Chatwoot. A partir daí a instância
// para de entregar mensagens (vínculo obrigatório no roteamento inbound).
func (h *AdminHandler) RemoveInstanceChatwootInbox(w http.ResponseWriter, r *http.Request) {
	instanceName := chi.URLParam(r, "instanceName")

	if err := h.adminService.RemoveInstanceChatwootInbox(r.Context(), instanceName); err != nil {
		if strings.Contains(err.Error(), "not found") {
			respondError(w, http.StatusNotFound, "INSTANCE_NOT_FOUND", err.Error(), nil)
			return
		}
		zap.L().Error("failed to remove instance chatwoot inbox", zap.String("instance", instanceName), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "REMOVE_INBOX_BINDING_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "unbound"})
}

// CreateWidgetInbox cria uma inbox tipo Website (widget público) na conta
// Chatwoot da Cartão Pro — usada pelo addon "Widget de Chat do Perfil
// Digital". Não persiste nada localmente: só fala com o Chatwoot e devolve
// os dados pro Next.js gravar em workspace_chat_inboxes.
func (h *AdminHandler) CreateWidgetInbox(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // name é opcional — usa default se ausente/corpo vazio

	resp, err := h.adminService.CreateWidgetInbox(r.Context(), body.Name)
	if err != nil {
		zap.L().Error("failed to create widget inbox", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "WIDGET_INBOX_FAILED", err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusCreated, resp)
}

// SyncWidgetWebhook migra o webhook de conta do widget pra incluir eventos
// novos (ex.: typing) caso tenham sido adicionados depois do provisionamento
// original. Não recria a inbox — seguro chamar a qualquer momento.
func (h *AdminHandler) SyncWidgetWebhook(w http.ResponseWriter, r *http.Request) {
	resp, err := h.adminService.SyncWidgetWebhook(r.Context())
	if err != nil {
		zap.L().Error("failed to sync widget webhook", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "WIDGET_WEBHOOK_SYNC_FAILED", err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, resp)
}

// CreateAgent vincula um profile do workspace como agente na conta Chatwoot
// da Cartão Pro — usado pelo combobox da aba "Usuários" do addon combo.
// Nunca devolve senha (gerada e descartada internamente); dispara o e-mail
// nativo de definição de senha do Chatwoot.
func (h *AdminHandler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	logger := zap.L().With(zap.String("handler", "create_agent"))

	var req services.CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid request body", nil)
		return
	}
	if req.Email == "" || req.Name == "" {
		respondError(w, http.StatusBadRequest, "MISSING_FIELDS", "email and name are required", nil)
		return
	}

	resp, err := h.adminService.CreateAgent(r.Context(), req)
	if err != nil {
		logger.Error("failed to create agent", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "CREATE_AGENT_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusCreated, resp)
}

// RemoveAgent desvincula um agente da conta Chatwoot da Cartão Pro.
func (h *AdminHandler) RemoveAgent(w http.ResponseWriter, r *http.Request) {
	agentIDStr := chi.URLParam(r, "agentId")
	logger := zap.L().With(zap.String("handler", "remove_agent"))

	agentID, err := strconv.Atoi(agentIDStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "INVALID_AGENT_ID", "agentId must be numeric", nil)
		return
	}

	if err := h.adminService.RemoveAgent(r.Context(), agentID); err != nil {
		logger.Error("failed to remove agent", zap.Int("agent_id", agentID), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "REMOVE_AGENT_FAILED", err.Error(), nil)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "removed"})
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
