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

	// Process delivery status updates.
	for _, status := range value.Statuses {
		eventID, ok := wamidstore.Get(status.ID)
		if !ok {
			continue // no trackable origin for this wamid
		}
		switch status.Status {
		case "sent":
			sendMetaCallback(metaSentCallback{
				Event:              "sent",
				EventID:            eventID,
				InstanceID:         instanceID,
				Timestamp:          unixStringToISO8601(status.Timestamp),
				Provider:           "meta",
				Wamid:              status.ID,
				PhoneNumberID:      phoneNumberID,
				ConversationID:     status.Conversation.ID,
				ConversationOrigin: status.Conversation.Origin.Type,
			})
		case "delivered":
			sendMetaCallback(metaDeliveredCallback{
				Event:         "delivered",
				EventID:       eventID,
				InstanceID:    instanceID,
				Timestamp:     unixStringToISO8601(status.Timestamp),
				Provider:      "meta",
				Wamid:         status.ID,
				PhoneNumberID: phoneNumberID,
				DeliveredAt:   unixStringToISO8601(status.Timestamp),
			})
		}
	}

	// Process inbound messages.
	// event_id is resolved via wa_message_logs (most recent sent/delivered to
	// this phone on this instance) — NOT via wamidstore, which only holds wamids
	// of messages the Bridge sent outbound.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, msg := range value.Messages {
		eventID, found, err := lookupEventIDByPhoneFn(instanceID, msg.From)
		if err != nil {
			zap.L().Warn("meta webhook: lookup event_id failed",
				zap.String("instance_id", instanceID),
				zap.Error(err),
			)
			// Continue with event_id=null rather than dropping the callback.
		}

		var eventIDPtr *string
		if found {
			eventIDPtr = &eventID
		}

		textBody := ""
		if msg.Type == "text" {
			textBody = msg.Text.Body
		}
		sendMetaCallback(metaMessageReceivedCallback{
			Event:         "message.received",
			EventID:       eventIDPtr,
			InstanceID:    instanceID,
			Timestamp:     now,
			Provider:      "meta",
			Wamid:         msg.ID,
			PhoneNumberID: phoneNumberID,
			Reply: metaWebhookReply{
				From:        msg.From,
				Text:        textBody,
				ReplyAt:     unixStringToISO8601(msg.Timestamp),
				MessageType: msg.Type,
			},
		})
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

type metaDeliveredCallback struct {
	Event         string `json:"event"`
	EventID       string `json:"event_id"`
	InstanceID    string `json:"instance_id"`
	Timestamp     string `json:"timestamp"`
	Provider      string `json:"provider"`
	Wamid         string `json:"wamid"`
	PhoneNumberID string `json:"phone_number_id"`
	DeliveredAt   string `json:"delivered_at"`
}

type metaWebhookReply struct {
	From        string `json:"from"`
	Text        string `json:"text"`
	ReplyAt     string `json:"reply_at"`
	MessageType string `json:"message_type"`
}

type metaMessageReceivedCallback struct {
	Event         string           `json:"event"`
	EventID       *string          `json:"event_id"`
	InstanceID    string           `json:"instance_id"`
	Timestamp     string           `json:"timestamp"`
	Provider      string           `json:"provider"`
	Wamid         string           `json:"wamid"`
	PhoneNumberID string           `json:"phone_number_id"`
	Reply         metaWebhookReply `json:"reply"`
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
