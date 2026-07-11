package models

import "time"

// SendMessageRequest payload para POST /notify/send
type SendMessageRequest struct {
	To       []string          `json:"to"`                 // números de telefone (ex: "+5511999999999")
	Type     string            `json:"type"`               // "text", "image", "video", "audio", "document"
	Text     string            `json:"text,omitempty"`
	MediaURL string            `json:"media_url,omitempty"`
	FileName string            `json:"filename,omitempty"`
	Instance string            `json:"instance,omitempty"` // opcional, usa default
	Metadata map[string]string `json:"metadata,omitempty"`
	// EventID, quando informado pelo caller, vira o id da notificação (em vez de um
	// UUID gerado internamente) — propagado como event_id nos callbacks
	// subsequentes (CallbackPayload.LogID), pro caller correlacionar a resposta
	// sem precisar de uma segunda chamada a GET /notify/status/:id. Só é honrado
	// quando To tem exatamente 1 destinatário (ver NotificationService.SendMessage)
	// — em envios em lote um único event_id não correlacionaria unicamente.
	// Mesmo princípio que já existia só pro provider Meta (NotifyRequest.EventID).
	EventID string `json:"event_id,omitempty"`
}

// SendTemplateRequest payload para POST /notify/template
type SendTemplateRequest struct {
	To         []string          `json:"to"`
	TemplateID string            `json:"template_id"`
	Variables  map[string]string `json:"variables,omitempty"`
	Instance   string            `json:"instance,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// NotificationResponse resposta dos endpoints de envio
type NotificationResponse struct {
	NotificationID string `json:"notification_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Status         string `json:"status"` // "queued"
	CorrelationID  string `json:"correlation_id"`
}

// NotificationStatusResponse para GET /notify/status/:id
type NotificationStatusResponse struct {
	NotificationID string     `json:"notification_id"`
	IdempotencyKey string     `json:"idempotency_key"`
	Status         string     `json:"status"` // "queued", "sent", "delivered", "failed"
	To             string     `json:"to"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	Error          string     `json:"error,omitempty"`
}

// NotifyRequest payload unificado para POST /notify/send com provider: "meta"
type NotifyRequest struct {
	Instance string       `json:"instance"`
	To       string       `json:"to"`
	Message  string       `json:"message"`
	Provider string       `json:"provider"`
	EventID  string       `json:"event_id"`
	Meta     *MetaPayload `json:"meta,omitempty"`
}

// MetaPayload campos específicos do provider Meta Cloud API
type MetaPayload struct {
	TemplateName string        `json:"template_name"`
	LanguageCode string        `json:"language_code"`
	Components   []interface{} `json:"components"`
}

// InstanceInfo dados de uma instância Evolution
type InstanceInfo struct {
	InstanceName string `json:"instance_name"`
	Status       string `json:"status"`
	PhoneNumber  string `json:"phone_number,omitempty"`
}
