package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"time"

	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/pkg/metaconfig"
	"github.com/linkkotech/bridge/pkg/wamidstore"
)

// Injectable for tests — production values are the defaults.
var (
	getMetaConfigFn  = metaconfig.GetMetaConfig
	metaGraphAPIHost = "https://graph.facebook.com"
	metaHTTPClient   = &http.Client{Timeout: 10 * time.Second}
	e164Re           = regexp.MustCompile(`^[1-9][0-9]{9,14}$`)
	// afterMetaSend é um hook só de teste — sendMetaMessage roda numa goroutine
	// fire-and-forget (handleMetaSend já respondeu 202 antes de chamá-la), então
	// os testes precisam de um jeito de saber quando ela terminou. No-op em produção.
	afterMetaSend = func() {}
)

// ── Meta Graph API request / response types ───────────────────────────────────

type metaAPIRequest struct {
	MessagingProduct string       `json:"messaging_product"`
	To               string       `json:"to"`
	Type             string       `json:"type"`
	Template         metaTemplate `json:"template"`
}

type metaTemplate struct {
	Name       string        `json:"name"`
	Language   metaLanguage  `json:"language"`
	Components []interface{} `json:"components,omitempty"`
}

type metaLanguage struct {
	Code string `json:"code"`
}

type metaSuccessResp struct {
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
	ConversationID     string `json:"conversation_id"`
	ConversationOrigin string `json:"conversation_origin"`
}

type metaErrorResp struct {
	Error struct {
		Code int    `json:"code"`
		Type string `json:"type"`
	} `json:"error"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

func (h *NotifyHandler) handleMetaSend(w http.ResponseWriter, _ *http.Request, req *models.NotifyRequest) {
	// 1. Validate meta field presence.
	if req.Meta == nil {
		respondError(w, http.StatusBadRequest, "META_PAYLOAD_REQUIRED", "meta_payload_required", nil)
		return
	}
	if req.Meta.TemplateName == "" || req.Meta.LanguageCode == "" {
		respondError(w, http.StatusBadRequest, "META_TEMPLATE_INCOMPLETE", "meta_template_incomplete", nil)
		return
	}

	// 2. Fetch Meta credentials (synchronous — errors are returned before 202).
	cfg, err := getMetaConfigFn(req.Instance)
	if errors.Is(err, metaconfig.ErrConfigNotFound) {
		respondError(w, http.StatusNotFound, "META_CONFIG_NOT_FOUND", "meta_config_not_found", nil)
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal_error", nil)
		return
	}

	// 3. Respond 202 immediately — o resultado do envio só fica nos logs
	// estruturados (não há mais callback para plataforma externa).
	respondJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})

	// 4. Fire-and-forget: call Meta.
	go func() {
		defer func() {
			if p := recover(); p != nil {
				zap.L().Error("meta send: recovered from panic",
					zap.String("instance", req.Instance),
					zap.Any("panic", p),
				)
			}
		}()
		sendMetaMessage(req, cfg)
	}()
}

// sendMetaMessage is the goroutine body — validates phone, calls Meta Graph API,
// and logs the outcome. O bridge só faz a ponte Chatwoot ↔ providers de WhatsApp;
// não há mais nenhum callback saindo para uma plataforma externa (Directus/Linkko).
func sendMetaMessage(req *models.NotifyRequest, cfg *metaconfig.MetaConfig) {
	defer afterMetaSend()

	// E.164 basic validation: digits only, 10–15 chars, no leading zero.
	if !e164Re.MatchString(req.To) {
		zap.L().Warn("meta send blocked: invalid phone format",
			zap.String("event_id", req.EventID),
			zap.String("instance", req.Instance),
			zap.String("phone_number_id", cfg.PhoneNumberID),
		)
		return
	}

	// Build Meta Graph API URL.
	version := os.Getenv("META_GRAPH_API_VERSION")
	if version == "" {
		version = "v19.0"
	}
	url := fmt.Sprintf("%s/%s/%s/messages", metaGraphAPIHost, version, cfg.PhoneNumberID)

	reqBody, err := json.Marshal(metaAPIRequest{
		MessagingProduct: "whatsapp",
		To:               req.To,
		Type:             "template",
		Template: metaTemplate{
			Name:       req.Meta.TemplateName,
			Language:   metaLanguage{Code: req.Meta.LanguageCode},
			Components: req.Meta.Components,
		},
	})
	if err != nil {
		zap.L().Error("meta send: marshal failed", zap.String("instance", req.Instance), zap.Error(err))
		return
	}

	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		zap.L().Error("meta send: build request failed", zap.String("instance", req.Instance), zap.Error(err))
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cfg.AccessToken) // never logged

	resp, err := metaHTTPClient.Do(httpReq)
	if err != nil {
		zap.L().Error("meta send: http error", zap.String("instance", req.Instance), zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var success metaSuccessResp
		if decErr := json.NewDecoder(resp.Body).Decode(&success); decErr != nil || len(success.Messages) == 0 {
			zap.L().Error("meta send: unexpected success body", zap.String("instance", req.Instance))
			return
		}
		// Guarda wamid → event_id ANTES de logar, pra que um evento de
		// delivered/message.received que chegue logo em seguida via webhook
		// já consiga resolver o mapeamento sem race.
		wamidstore.Set(success.Messages[0].ID, req.EventID)
		zap.L().Info("meta send: sent",
			zap.String("event_id", req.EventID),
			zap.String("instance", req.Instance),
			zap.String("wamid", success.Messages[0].ID),
			zap.String("phone_number_id", cfg.PhoneNumberID),
			zap.String("conversation_id", success.ConversationID),
			zap.String("conversation_origin", success.ConversationOrigin),
		)
		return
	}

	// 4xx / 5xx — extract error code and map to canonical reason.
	var apiErr metaErrorResp
	_ = json.NewDecoder(resp.Body).Decode(&apiErr)
	zap.L().Warn("meta send blocked: meta api error",
		zap.String("event_id", req.EventID),
		zap.String("instance", req.Instance),
		zap.String("phone_number_id", cfg.PhoneNumberID),
		zap.Int("meta_error_code", apiErr.Error.Code),
		zap.String("meta_error_type", apiErr.Error.Type),
		zap.String("reason", metaErrorCodeToReason(apiErr.Error.Code)),
	)
}

func metaErrorCodeToReason(code int) string {
	switch code {
	case 190:
		return "invalid_token"
	case 100:
		return "invalid_phone_number_id"
	case 132000:
		return "template_not_found"
	case 132001:
		return "template_paused"
	case 131026:
		return "number_not_on_whatsapp"
	case 131047:
		return "outside_24h_window"
	case 130429:
		return "meta_rate_limit"
	default:
		return "unknown_meta_error"
	}
}
