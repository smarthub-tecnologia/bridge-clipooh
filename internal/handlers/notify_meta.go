package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	getMetaConfigFn    = metaconfig.GetMetaConfig
	metaGraphAPIHost   = "https://graph.facebook.com"
	metaHTTPClient     = &http.Client{Timeout: 10 * time.Second}
	metaCallbackClient = &http.Client{Timeout: 5 * time.Second}
	e164Re             = regexp.MustCompile(`^[1-9][0-9]{9,14}$`)
)

// ── Meta callback payload types ───────────────────────────────────────────────

type metaSentCallback struct {
	Event              string `json:"event"`
	EventID            string `json:"event_id"`
	InstanceID         string `json:"instance_id"`
	Timestamp          string `json:"timestamp"`
	Provider           string `json:"provider"`
	Wamid              string `json:"wamid"`
	PhoneNumberID      string `json:"phone_number_id"`
	ConversationID     string `json:"conversation_id"`
	ConversationOrigin string `json:"conversation_origin"`
}

type metaBlockedCallback struct {
	Event         string  `json:"event"`
	EventID       string  `json:"event_id"`
	InstanceID    string  `json:"instance_id"`
	Timestamp     string  `json:"timestamp"`
	Provider      string  `json:"provider"`
	Wamid         *string `json:"wamid"` // always null
	PhoneNumberID string  `json:"phone_number_id"`
	MetaErrorCode int     `json:"meta_error_code,omitempty"`
	MetaErrorType string  `json:"meta_error_type,omitempty"`
	Reason        string  `json:"reason"`
}

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

	// 3. Respond 202 immediately — errors from Meta arrive asynchronously.
	respondJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})

	// 4. Fire-and-forget: call Meta and dispatch callback.
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
// then dispatches the appropriate callback to the platform.
func sendMetaMessage(req *models.NotifyRequest, cfg *metaconfig.MetaConfig) {
	now := time.Now().UTC().Format(time.RFC3339)

	// E.164 basic validation: digits only, 10–15 chars, no leading zero.
	if !e164Re.MatchString(req.To) {
		sendMetaCallback(metaBlockedCallback{
			Event:         "blocked_meta_error",
			EventID:       req.EventID,
			InstanceID:    req.Instance,
			Timestamp:     now,
			Provider:      "meta",
			PhoneNumberID: cfg.PhoneNumberID,
			Reason:        "invalid_phone_format",
		})
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
		// Set wamid → event_id BEFORE dispatching the callback so that any
		// immediately-following delivered/message.received webhook event can
		// resolve the mapping without a race.
		wamidstore.Set(success.Messages[0].ID, req.EventID)
		sendMetaCallback(metaSentCallback{
			Event:              "sent",
			EventID:            req.EventID,
			InstanceID:         req.Instance,
			Timestamp:          now,
			Provider:           "meta",
			Wamid:              success.Messages[0].ID,
			PhoneNumberID:      cfg.PhoneNumberID,
			ConversationID:     success.ConversationID,
			ConversationOrigin: success.ConversationOrigin,
		})
		return
	}

	// 4xx / 5xx — extract error code and map to canonical reason.
	var apiErr metaErrorResp
	_ = json.NewDecoder(resp.Body).Decode(&apiErr)
	sendMetaCallback(metaBlockedCallback{
		Event:         "blocked_meta_error",
		EventID:       req.EventID,
		InstanceID:    req.Instance,
		Timestamp:     now,
		Provider:      "meta",
		PhoneNumberID: cfg.PhoneNumberID,
		MetaErrorCode: apiErr.Error.Code,
		MetaErrorType: apiErr.Error.Type,
		Reason:        metaErrorCodeToReason(apiErr.Error.Code),
	})
}

// sendMetaCallback POSTs payload to the platform callback endpoint, signed with
// HMAC-SHA256 using CALLBACKS_DIRECTUS_WEBHOOK_SECRET.
func sendMetaCallback(payload any) {
	if os.Getenv("CALLBACKS_ENABLED") == "false" {
		return
	}
	webhookURL := os.Getenv("CALLBACKS_DIRECTUS_WEBHOOK_URL")
	if webhookURL == "" {
		return
	}
	secret := os.Getenv("CALLBACKS_DIRECTUS_WEBHOOK_SECRET")

	body, err := json.Marshal(payload)
	if err != nil {
		zap.L().Error("meta callback: marshal failed", zap.Error(err))
		return
	}

	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		zap.L().Error("meta callback: build request failed", zap.Error(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Bridge-Signature", hex.EncodeToString(mac.Sum(nil)))
	}

	cbResp, err := metaCallbackClient.Do(req)
	if err != nil {
		zap.L().Error("meta callback: send failed", zap.Error(err))
		return
	}
	defer cbResp.Body.Close()

	if cbResp.StatusCode >= 400 {
		zap.L().Error("meta callback: destination returned error", zap.Int("status", cbResp.StatusCode))
	}
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
