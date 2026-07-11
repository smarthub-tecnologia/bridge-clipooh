package handlers

import (
	"encoding/json"
	"testing"
	"time"
)

// TestCallbackShape_Sent verifies the contract v1.1 §4.2 shape for the "sent" event:
// all 9 top-level fields must be present, including conversation_id and
// conversation_origin even when they are empty strings.
func TestCallbackShape_Sent(t *testing.T) {
	cb := metaSentCallback{
		Event:              "sent",
		EventID:            "evt-001",
		InstanceID:         "inst-001",
		Timestamp:          "2024-05-06T00:00:00Z",
		Provider:           "meta",
		Wamid:              "wamid.abc",
		PhoneNumberID:      "1098",
		ConversationID:     "",
		ConversationOrigin: "",
	}
	b, err := json.Marshal(cb)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	required := []string{
		"event", "event_id", "instance_id", "timestamp", "provider",
		"wamid", "phone_number_id", "conversation_id", "conversation_origin",
	}
	for _, key := range required {
		if _, ok := m[key]; !ok {
			t.Errorf("missing JSON field: %s", key)
		}
	}
}

// TestCallbackShape_Delivered verifies the "delivered" event shape and that
// delivered_at is parseable as RFC3339.
func TestCallbackShape_Delivered(t *testing.T) {
	cb := metaDeliveredCallback{
		Event:         "delivered",
		EventID:       "evt-002",
		InstanceID:    "inst-001",
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		Provider:      "meta",
		Wamid:         "wamid.def",
		PhoneNumberID: "1098",
		DeliveredAt:   time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(cb)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"event", "event_id", "instance_id", "timestamp", "provider", "wamid", "phone_number_id", "delivered_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing JSON field: %s", key)
		}
	}
	deliveredAt, _ := m["delivered_at"].(string)
	if _, parseErr := time.Parse(time.RFC3339, deliveredAt); parseErr != nil {
		t.Errorf("delivered_at %q is not RFC3339: %v", deliveredAt, parseErr)
	}
}

// TestCallbackShape_BlockedMetaError verifies the "blocked_meta_error" shape:
// wamid must be JSON null, and meta_error_code must be a positive number.
func TestCallbackShape_BlockedMetaError(t *testing.T) {
	errCode := 190
	cb := metaBlockedCallback{
		Event:         "blocked_meta_error",
		EventID:       "evt-003",
		InstanceID:    "inst-001",
		Timestamp:     "2024-05-06T00:00:00Z",
		Provider:      "meta",
		Wamid:         nil,
		PhoneNumberID: "1098",
		MetaErrorCode: errCode,
		MetaErrorType: "OAuthException",
		Reason:        "invalid_token",
	}
	b, err := json.Marshal(cb)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"event", "event_id", "instance_id", "timestamp", "provider", "wamid", "phone_number_id", "reason"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing JSON field: %s", key)
		}
	}
	if m["wamid"] != nil {
		t.Errorf("wamid must be JSON null, got %v", m["wamid"])
	}
	code, ok := m["meta_error_code"].(float64)
	if !ok || code <= 0 {
		t.Errorf("meta_error_code must be a positive number, got %v", m["meta_error_code"])
	}
}

// TestCallbackShape_MessageReceived verifies the "message.received" shape:
// reply sub-fields must be present; non-text messages must have text == "".
func TestCallbackShape_MessageReceived(t *testing.T) {
	evtID := "evt-004"
	cb := metaMessageReceivedCallback{
		Event:         "message.received",
		EventID:       &evtID,
		InstanceID:    "inst-001",
		Timestamp:     "2024-05-06T00:00:00Z",
		Provider:      "meta",
		Wamid:         "wamid.ghi",
		PhoneNumberID: "1098",
		Reply: metaWebhookReply{
			From:        "5511999999999",
			Text:        "",
			ReplyAt:     "2024-05-06T00:00:05Z",
			MessageType: "audio",
		},
	}
	b, err := json.Marshal(cb)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"event", "event_id", "instance_id", "timestamp", "provider", "wamid", "phone_number_id", "reply"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing JSON field: %s", key)
		}
	}
	reply, _ := m["reply"].(map[string]interface{})
	for _, key := range []string{"from", "text", "reply_at", "message_type"} {
		if _, ok := reply[key]; !ok {
			t.Errorf("missing reply JSON field: %s", key)
		}
	}
	if reply["message_type"] != "audio" {
		t.Errorf("message_type = %v, want audio", reply["message_type"])
	}
	if reply["text"] != "" {
		t.Errorf("text = %v, want empty string for non-text message", reply["text"])
	}
}
