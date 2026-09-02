package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/linkkotech/bridge/pkg/metaconfig"
	"github.com/linkkotech/bridge/pkg/wamidstore"
)

// Injectable for tests.
var (
	getInstanceIDByPhoneNumberIDFn = metaconfig.GetInstanceIDByPhoneNumberID
	lookupEventIDByPhoneFn         = metaconfig.LookupEventIDByPhone
	// afterMetaWebhookEvent é um hook só de teste — processMetaEvent roda numa
	// goroutine fire-and-forget (HandleEvent já respondeu 200 antes de
	// chamá-la), então os testes precisam de um jeito de saber quando ela
	// terminou. No-op em produção.
	afterMetaWebhookEvent = func() {}
)

// ── Handler ───────────────────────────────────────────────────────────────────

type MetaWebhookHandler struct{}

func NewMetaWebhookHandler() *MetaWebhookHandler {
	return &MetaWebhookHandler{}
}

// VerifyWebhook handles GET /api/v1/meta/webhook — the hub challenge flow
// that Meta uses to verify the webhook endpoint before activating it.
func (h *MetaWebhookHandler) VerifyWebhook(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mode      := q.Get("hub.mode")
	token     := q.Get("hub.verify_token")
	challenge := q.Get("hub.challenge")

	expected := os.Getenv("META_WEBHOOK_VERIFY_TOKEN")
	if expected != "" && mode == "subscribe" && token == expected {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(challenge))
		return
	}
	w.WriteHeader(http.StatusForbidden)
}

// HandleEvent handles POST /api/v1/meta/webhook — delivery of async events
// (message delivered, inbound message received) from Meta Cloud API.
//
// Responds 403 when the X-Hub-Signature-256 header is absent or invalid —
// this is the only case where Meta receives a non-200. For all other
// processing outcomes the handler returns 200 (Meta would retry on 5xx).
func (h *MetaWebhookHandler) HandleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Security gate — executes before any field access.
	if !verifyMetaSignature(body, r.Header.Get("X-Hub-Signature-256")) {
		zap.L().Warn("meta webhook: signature invalid or missing — possible forgery attempt")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	var payload metaWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Respond 200 before any processing — Meta must never receive a 4xx/5xx.
	w.WriteHeader(http.StatusOK)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				zap.L().Error("meta webhook: recovered from panic", zap.Any("panic", p))
			}
		}()
		processMetaEvent(payload)
	}()
}

// ── Event processing ──────────────────────────────────────────────────────────

func processMetaEvent(payload metaWebhookPayload) {
	defer afterMetaWebhookEvent()

	if len(payload.Entry) == 0 || len(payload.Entry[0].Changes) == 0 {
		return
	}
	value := payload.Entry[0].Changes[0].Value
	phoneNumberID := value.Metadata.PhoneNumberID

	instanceID, err := getInstanceIDByPhoneNumberIDFn(phoneNumberID)
	if err != nil {
		// Unknown or unconfigured instance — 200 already sent, silently ignore.
		return
	}

	// Log delivery status updates — não há mais callback para plataforma
	// externa (Directus/Linkko); o bridge só faz a ponte Chatwoot ↔ providers
	// de WhatsApp, então isso só fica registrado nos logs estruturados para
	// debug/auditoria (correlacionar wamid ↔ event_id).
	for _, status := range value.Statuses {
		eventID, ok := wamidstore.Get(status.ID)
		if !ok {
			continue // no trackable origin for this wamid
		}
		switch status.Status {
		case "sent":
			zap.L().Info("meta message status: sent",
				zap.String("event_id", eventID),
				zap.String("instance_id", instanceID),
				zap.String("wamid", status.ID),
				zap.String("phone_number_id", phoneNumberID),
				zap.String("conversation_id", status.Conversation.ID),
				zap.String("conversation_origin", status.Conversation.Origin.Type),
				zap.String("timestamp", unixStringToISO8601(status.Timestamp)),
			)
		case "delivered":
			zap.L().Info("meta message status: delivered",
				zap.String("event_id", eventID),
				zap.String("instance_id", instanceID),
				zap.String("wamid", status.ID),
				zap.String("phone_number_id", phoneNumberID),
				zap.String("delivered_at", unixStringToISO8601(status.Timestamp)),
			)
		}
	}

	// Log inbound messages received via Meta Cloud API.
	// event_id is resolved via wa_message_logs (most recent sent/delivered to
	// this phone on this instance) — NOT via wamidstore, which only holds wamids
	// of messages the Bridge sent outbound.
	for _, msg := range value.Messages {
		eventID, found, err := lookupEventIDByPhoneFn(instanceID, msg.From)
		if err != nil {
			zap.L().Warn("meta webhook: lookup event_id failed",
				zap.String("instance_id", instanceID),
				zap.Error(err),
			)
			// Continue with event_id="" rather than dropping the log entry.
		}

		textBody := ""
		if msg.Type == "text" {
			textBody = msg.Text.Body
		}
		logger := zap.L().With(
			zap.String("instance_id", instanceID),
			zap.String("wamid", msg.ID),
			zap.String("phone_number_id", phoneNumberID),
			zap.String("from", msg.From),
			zap.String("message_type", msg.Type),
			zap.String("reply_at", unixStringToISO8601(msg.Timestamp)),
		)
		if found {
			logger = logger.With(zap.String("event_id", eventID))
		}
		logger.Info("meta message received", zap.String("text", textBody))
	}
}

// ── Payload types ─────────────────────────────────────────────────────────────

type metaWebhookPayload struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Metadata struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Messages []struct {
					ID        string `json:"id"`
					From      string `json:"from"`
					Timestamp string `json:"timestamp"`
					Type      string `json:"type"`
					Text      struct {
						Body string `json:"body"`
					} `json:"text"`
				} `json:"messages"`
				Statuses []struct {
					ID           string `json:"id"`
					Status       string `json:"status"`
					Timestamp    string `json:"timestamp"`
					Conversation struct {
						ID     string `json:"id"`
						Origin struct {
							Type string `json:"type"`
						} `json:"origin"`
					} `json:"conversation"`
				} `json:"statuses"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// verifyMetaSignature validates the X-Hub-Signature-256 header that Meta
// signs on every webhook POST using META_APP_SECRET (the app secret from
// Meta for Developers → App → Settings → Basic).
// Returns false when the secret env is empty, the header is absent, or the
// digest does not match — in all cases the request must be rejected with 403.
func verifyMetaSignature(body []byte, header string) bool {
	secret := os.Getenv("META_APP_SECRET")
	if secret == "" || header == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := prefix + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(header), []byte(expected))
}

func unixStringToISO8601(ts string) string {
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ts // fallback: return as-is
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}
