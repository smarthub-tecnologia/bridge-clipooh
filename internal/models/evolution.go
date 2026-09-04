package models

import (
	"encoding/json"
	"time"
)

type EvolutionInstance struct {
	ID               string                 `json:"id"`
	InstanceID       string                 `json:"instance_id"`
	InstanceName     string                 `json:"instance_name"`
	APIKey           *string                `json:"api_key,omitempty"`
	BaseURL          *string                `json:"base_url,omitempty"`
	Status           string                 `json:"status"`
	QRCode           *string                `json:"qr_code,omitempty"`
	PhoneNumber      *string                `json:"phone_number,omitempty"`
	WebhookURL       *string                `json:"webhook_url,omitempty"`
	WebhookEvents    map[string]interface{} `json:"webhook_events,omitempty"`
	ConnectedAt      *time.Time             `json:"connected_at,omitempty"`
	DisconnectedAt   *time.Time             `json:"disconnected_at,omitempty"`
	DisconnectReason *string                `json:"disconnect_reason,omitempty"`
	MessagesSent     int                    `json:"messages_sent"`
	MessagesReceived int                    `json:"messages_received"`
	Config           map[string]interface{} `json:"config,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
	// Vínculo com a inbox Chatwoot (obrigatório para entrega de mensagens).
	// Preenchido pelo dashboard no provisionamento/edição da instância.
	ChatwootInboxID            *int      `json:"chatwoot_inbox_id,omitempty" db:"chatwoot_inbox_id"`
	ChatwootInboxName          *string   `json:"chatwoot_inbox_name,omitempty" db:"chatwoot_inbox_name"`
	ChatwootInboxWebhookSecret *string   `json:"chatwoot_inbox_webhook_secret,omitempty" db:"chatwoot_inbox_webhook_secret"`
	ChatwootInboxIdentifier    *string   `json:"chatwoot_inbox_identifier,omitempty" db:"chatwoot_inbox_identifier"`
	CreatedAt                  time.Time `json:"created_at"`
	UpdatedAt                  time.Time `json:"updated_at"`
}

type EvolutionAdvancedSettings struct {
	AlwaysOnline  bool   `json:"alwaysOnline"`
	IgnoreGroups  bool   `json:"ignoreGroups"`
	IgnoreStatus  bool   `json:"ignoreStatus"`
	MsgRejectCall string `json:"msgRejectCall"`
	ReadMessages  bool   `json:"readMessages"`
	RejectCall    bool   `json:"rejectCall"`
}

// EvolutionCreateInstanceRequest payload real para Evolution GO
type EvolutionCreateInstanceRequest struct {
	Name             string                     `json:"name"`
	InstanceID       string                     `json:"instanceId"`
	Token            string                     `json:"token"`
	AdvancedSettings *EvolutionAdvancedSettings `json:"advancedSettings,omitempty"`
}

// EvolutionConnectRequest payload para /instance/connect (Evolution GO ConnectStruct)
// A instância é identificada pelo header apikey (token da instância), não por campo no body.
type EvolutionConnectRequest struct {
	Immediate       bool     `json:"immediate"`
	WebhookUrl      string   `json:"webhookUrl"`
	Subscribe       []string `json:"subscribe"`
	WebsocketEnable string   `json:"websocketEnable,omitempty"`
	Phone           string   `json:"phone,omitempty"`
}

// EvolutionCreateInstanceResponse resposta real para Evolution GO
type EvolutionCreateInstanceResponse struct {
	InstanceID string `json:"instanceId"`
	Name       string `json:"name"`
	Token      string `json:"token"`
}

// EvolutionQRCode QR code para conexão
type EvolutionQRCode struct {
	Base64 string `json:"base64"`
	Count  int    `json:"count"`
}

// EvolutionQuotedStruct referência para reply (quoted message)
type EvolutionQuotedStruct struct {
	MessageID   string `json:"messageId,omitempty"`
	Participant string `json:"participant,omitempty"`
}

// EvolutionSendTextRequest payload para /send/text (Evolution GO TextStruct)
type EvolutionSendTextRequest struct {
	Number       string                 `json:"number"`
	Text         string                 `json:"text"`
	Delay        int                    `json:"delay,omitempty"`
	FormatJid    bool                   `json:"formatJid,omitempty"`
	MentionAll   bool                   `json:"mentionAll,omitempty"`
	MentionedJid []string               `json:"mentionedJid,omitempty"`
	Quoted       *EvolutionQuotedStruct `json:"quoted,omitempty"`
}

// EvolutionSendMediaRequest payload para /send/media (Evolution GO MediaStruct)
type EvolutionSendMediaRequest struct {
	Number       string                 `json:"number"`
	URL          string                 `json:"url"`
	Type         string                 `json:"type"`
	Filename     string                 `json:"filename,omitempty"`
	Caption      string                 `json:"caption,omitempty"`
	Delay        int                    `json:"delay,omitempty"`
	FormatJid    bool                   `json:"formatJid,omitempty"`
	MentionAll   bool                   `json:"mentionAll,omitempty"`
	MentionedJid []string               `json:"mentionedJid,omitempty"`
	Quoted       *EvolutionQuotedStruct `json:"quoted,omitempty"`
}

// EvolutionSendResponse resposta de envio
type EvolutionSendResponse struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"`
}

// EvolutionSendResponseRaw objeto retornado pelo /send/text e /send/media
// Ex: {"data": {"Info": {"ID": "..."}, ...}, "message": "success"}
type EvolutionSendResponseRaw struct {
	Data    EvolutionMessageData `json:"data"`
	Message string               `json:"message"`
}

// EvolutionEditRequest payload para /message/edit
type EvolutionEditRequest struct {
	MessageID string `json:"messageId"`
	Message   string `json:"message"`
	Chat      string `json:"chat"`
}

// EvolutionEditResponse resposta de edição
type EvolutionEditResponse struct {
	Status string `json:"status"`
}

// EvolutionWebhook é o payload recebido da Evolution GO via webhook
// Estrutura real baseada no source da Evolution GO (auth_middleware Auth)
type EvolutionWebhook struct {
	Event        string          `json:"event"`
	Data         json.RawMessage `json:"data"`
	InstanceName string          `json:"-"` // preenchido pelo handler via query param
}

// EvolutionMessageData contém os dados de uma mensagem (usado em webhook e em send response)
type EvolutionMessageData struct {
	Info    EvolutionMessageInfo `json:"Info"`
	Message EvolutionMessageBody `json:"Message"`
	Status  string               `json:"status,omitempty"` // para eventos de update
}

// EvolutionMessageInfo metadados da mensagem (campo "Info" do payload Evolution GO,
// que embute whatsmeow_types.MessageInfo).
//
// Type é uma classificação grosseira ("text" ou "media") — não o nome do
// sub-tipo da mensagem. Para mídia, o sub-tipo real vem em MediaType
// ("image", "video", "gif", "audio", "ptt", "document", "sticker"); mensagens
// de vídeo com reprodução em loop chegam como MediaType "gif" (vídeo mp4 com
// gifPlayback: true no proto), não como um tipo próprio.
type EvolutionMessageInfo struct {
	Chat      string `json:"Chat"`      // JID do contato: "5511999@s.whatsapp.net"
	Sender    string `json:"Sender"`    // JID do remetente (pode diferir em grupos)
	IsFromMe  bool   `json:"IsFromMe"`  // true = mensagem enviada pelo bot
	ID        string `json:"ID"`        // ID único da mensagem
	Type      string `json:"Type"`      // "text" ou "media"
	MediaType string `json:"MediaType"` // "image", "video", "gif", "audio", "ptt", "document", "sticker"
	PushName  string `json:"PushName"`  // nome do contato no WhatsApp
}

// EvolutionMessageBody conteúdo da mensagem (campo "Message" do payload Evolution GO)
// Os campos são mutuamente exclusivos conforme o Type da mensagem
type EvolutionMessageBody struct {
	Conversation        string                 `json:"conversation,omitempty"`
	ExtendedTextMessage *EvolutionExtendedText `json:"extendedTextMessage,omitempty"`
	ImageMessage        *EvolutionMediaContent `json:"imageMessage,omitempty"`
	VideoMessage        *EvolutionMediaContent `json:"videoMessage,omitempty"`
	AudioMessage        *EvolutionMediaContent `json:"audioMessage,omitempty"`
	DocumentMessage     *EvolutionMediaContent `json:"documentMessage,omitempty"`
}

// Text retorna o texto da mensagem independente do tipo
func (m EvolutionMessageBody) Text() string {
	if m.Conversation != "" {
		return m.Conversation
	}
	if m.ExtendedTextMessage != nil {
		return m.ExtendedTextMessage.Text
	}
	return ""
}

// MediaContent retorna o conteúdo de mídia, se houver
func (m EvolutionMessageBody) MediaContent() *EvolutionMediaContent {
	if m.ImageMessage != nil {
		return m.ImageMessage
	}
	if m.VideoMessage != nil {
		return m.VideoMessage
	}
	if m.AudioMessage != nil {
		return m.AudioMessage
	}
	if m.DocumentMessage != nil {
		return m.DocumentMessage
	}
	return nil
}

// EvolutionExtendedText texto com formatação/contexto
type EvolutionExtendedText struct {
	Text string `json:"text"`
}

// EvolutionMediaContent conteúdo de mídia (imagem, vídeo, áudio, documento).
// URL aponta para o blob criptografado no CDN do WhatsApp — MediaKey (e os
// demais campos abaixo, todos base64 de []byte no proto original) são
// necessários para derivar as chaves AES/HMAC e descriptografar o arquivo
// (ver whatsapp_media.go).
type EvolutionMediaContent struct {
	URL           string `json:"url"`
	Mimetype      string `json:"mimetype"`
	FileName      string `json:"fileName,omitempty"`
	Caption       string `json:"caption,omitempty"`
	MediaKey      string `json:"mediaKey,omitempty"`      // base64
	FileEncSHA256 string `json:"fileEncSha256,omitempty"` // base64 — hash do blob cifrado
	FileSHA256    string `json:"fileSha256,omitempty"`    // base64 — hash do arquivo decifrado
	FileLength    int64  `json:"fileLength,omitempty"`
	DirectPath    string `json:"directPath,omitempty"`
	GifPlayback   bool   `json:"gifPlayback,omitempty"` // videoMessage com loop = "gif" no MediaType
}

// EvolutionInstanceInfo resposta do GET /instance/info/{instanceId}
type EvolutionInstanceInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Webhook   string `json:"webhook"`
	Events    string `json:"events"`
	Connected bool   `json:"connected"`
}

// EvolutionInstanceInfoResponse wrapper da resposta
type EvolutionInstanceInfoResponse struct {
	Message string                `json:"message"`
	Data    EvolutionInstanceInfo `json:"data"`
}

// Aliases mantidos para compatibilidade interna
type EvolutionData = EvolutionMessageData
