package models

import "time"

type ChatwootInbox struct {
	ID                string    `json:"id" db:"id"`
	ChatwootAccountID int       `json:"chatwoot_account_id" db:"chatwoot_account_id"`
	InboxID           int       `json:"inbox_id" db:"inbox_id"`
	InboxName         string    `json:"inbox_name" db:"inbox_name"`
	InboxType         string    `json:"inbox_type" db:"inbox_type"`
	WebhookConfigured bool      `json:"webhook_configured" db:"webhook_configured"`
	CreatedAt         time.Time `json:"created_at" db:"created_at"`
}

// ChatwootCreateAccountRequest payload para Platform API do Chatwoot
type ChatwootCreateAccountRequest struct {
	Name   string `json:"name"`
	Locale string `json:"locale,omitempty"`
	Domain string `json:"domain,omitempty"`
}

type ChatwootCreateUserRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type ChatwootCreateUserResponse struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	AccessToken string `json:"access_token"`
	// Accounts — contas às quais este usuário já está vinculado ANTES desta
	// chamada (o jbuilder da Platform API sempre devolve isso). Se vier vazio,
	// o usuário é novo de verdade; se vier não-vazio, a Platform API reaproveitou
	// um usuário já existente em outra(s) conta(s) (User.from_email). Usado como
	// guarda de segurança: só é seguro fazer rollback (DeleteUser) se este campo
	// veio vazio na criação — do contrário estaríamos apagando o acesso de
	// alguém a contas que não têm nada a ver com esta operação.
	Accounts []ChatwootUserAccountRef `json:"accounts"`
}

type ChatwootUserAccountRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type ChatwootAddUserToAccountRequest struct {
	UserID int    `json:"user_id"`
	Role   string `json:"role"` // "administrator" or "agent"
}

// ChatwootCreateAccountResponse resposta da criação de conta
type ChatwootCreateAccountResponse struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type ChatwootChannelApi struct {
	Type          string `json:"type"`
	WebhookURL    string `json:"webhook_url,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// ChatwootCreateInboxRequest payload para criar inbox via API
type ChatwootCreateInboxRequest struct {
	Name    string             `json:"name"`
	Channel ChatwootChannelApi `json:"channel"`
}

// ChatwootCreateInboxResponse resposta
type ChatwootCreateInboxResponse struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Secret string `json:"secret,omitempty"` // Chatwoot auto-gera via has_secure_token; retornado apenas para admins
	// ChannelType é o tipo real do canal no Chatwoot ("Channel::Api",
	// "Channel::Whatsapp", "Channel::WebWidget"...). Vem no GET /inboxes e é o
	// que permite o bridge distinguir a inbox "api" (a dele) de uma inbox
	// WhatsApp/Meta — o rótulo inbox_type gravado localmente nem sempre
	// corresponde ao tipo real do Chatwoot.
	ChannelType string `json:"channel_type,omitempty"`
}

// ChatwootChannelWebWidget payload do canal "Website" (widget público) — ao
// contrário do canal "api" (WhatsApp), não tem webhook próprio: mensagens
// chegam via REST normal, sem callback. WebsiteURL é só metadado exibido no
// painel do Chatwoot, não afeta o funcionamento do widget.
type ChatwootChannelWebWidget struct {
	Type       string `json:"type"`
	WebsiteURL string `json:"website_url,omitempty"`
}

// ChatwootCreateWebsiteInboxRequest payload para criar inbox tipo Website via API
type ChatwootCreateWebsiteInboxRequest struct {
	Name    string                   `json:"name"`
	Channel ChatwootChannelWebWidget `json:"channel"`
}

// ChatwootCreateWebsiteInboxResponse resposta da criação de inbox Website.
// NOTA: shape assumido a partir da documentação pública do Chatwoot
// (website_token no nível da inbox; hmac_token só vem preenchido se
// "Enable Identity Validation" já estiver ativo na inbox) — não validado
// contra uma instância real ainda. Campos extras retornados pelo Chatwoot são
// ignorados pelo json.Decode, então divergências aditivas não quebram nada;
// se website_token vier em outro campo/nesting, ajustar aqui.
type ChatwootCreateWebsiteInboxResponse struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	WebsiteToken  string `json:"website_token,omitempty"`
	HMACToken     string `json:"hmac_token,omitempty"`
	HMACMandatory bool   `json:"hmac_mandatory,omitempty"`
}

// ── Account-level webhooks (Fase B — tempo real do widget) ────────────────────
//
// Channel::WebWidget não tem webhook por-inbox como Channel::Api (confirmado
// lendo app/listeners/webhook_listener.rb v4.15.1 — deliver_api_inbox_webhooks
// só roda pra channel_type == 'Channel::Api'). O único mecanismo que dispara
// pra eventos de uma inbox web_widget é o webhook DE CONTA
// (POST /api/v1/accounts/{id}/webhooks) — dispara pra TODAS as inboxes da
// conta, inclusive o combo WhatsApp; o filtro por inbox_id é feito no
// consumidor (Next.js), não aqui.

// ChatwootCreateWebhookRequest payload para criar um webhook de conta.
type ChatwootCreateWebhookRequest struct {
	Webhook ChatwootWebhookParams `json:"webhook"`
}

type ChatwootWebhookParams struct {
	URL           string   `json:"url"`
	Subscriptions []string `json:"subscriptions"`
}

// ChatwootWebhookPayload é o shape de um webhook individual, igual retornado
// tanto na criação quanto na listagem (_webhook.json.jbuilder).
type ChatwootWebhookPayload struct {
	ID            int      `json:"id"`
	Name          string   `json:"name,omitempty"`
	URL           string   `json:"url"`
	AccountID     int      `json:"account_id"`
	Subscriptions []string `json:"subscriptions"`
	// Secret é gerado pelo Rails via has_secure_token — só vem preenchido na
	// resposta de criação/listagem pra quem tem acesso de admin da conta.
	Secret string `json:"secret,omitempty"`
}

type ChatwootCreateWebhookResponse struct {
	Payload struct {
		Webhook ChatwootWebhookPayload `json:"webhook"`
	} `json:"payload"`
}

type ChatwootListWebhooksResponse struct {
	Payload struct {
		Webhooks []ChatwootWebhookPayload `json:"webhooks"`
	} `json:"payload"`
}

// ── Custom Attribute Definitions (perfil de origem no widget/SDR) ────────
//
// Precisam existir na conta ANTES de qualquer conversation.custom_attributes
// gravar profile_id/profile_slug — o Chatwoot aceita o POST em
// /conversations/{id}/custom_attributes mesmo sem a Definition cadastrada,
// mas descarta a key silenciosamente (não dá erro). Endpoint é Application
// API (mesmo account_id/token dos outros métodos deste arquivo, não Platform
// API). Shape conforme a doc pública do Chatwoot
// (https://www.chatwoot.com/developers/api/#tag/Custom-Attribute).

// ChatwootCustomAttributeDefinition representa um item de "Configurações >
// Atributos Personalizados" no painel.
type ChatwootCustomAttributeDefinition struct {
	ID                   int      `json:"id,omitempty"`
	AttributeDisplayName string   `json:"attribute_display_name"`
	AttributeKey         string   `json:"attribute_key,omitempty"`
	AttributeDisplayType string   `json:"attribute_display_type"` // "text", "number", "list", etc.
	AttributeModel       string   `json:"attribute_model"`        // "conversation_attribute" ou "contact_attribute"
	AttributeDescription string   `json:"attribute_description,omitempty"`
	AttributeValues      []string `json:"attribute_values,omitempty"`
}

// ChatwootPlatformAccessTokenRequest gera token de acesso para uma conta
type ChatwootPlatformAccessTokenRequest struct {
}

// ChatwootPlatformAccessTokenResponse
type ChatwootPlatformAccessTokenResponse struct {
	AccessToken string `json:"access_token"`
}

// ChatwootContact representa um contato no Chatwoot
type ChatwootContact struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	PhoneNumber string `json:"phone_number"`
}

// ChatwootContactSearchResponse resposta da busca de contato
type ChatwootContactSearchResponse struct {
	Payload []ChatwootContact `json:"payload"`
}

// ChatwootContactConflictResponse é o body retornado pelo Chatwoot em 422
// quando o contato já existe. O campo ExistingContact contém o contato existente.
type ChatwootContactConflictResponse struct {
	Message         string          `json:"message"`
	ExistingContact ChatwootContact `json:"existing_contact"`
}

// ChatwootConversation representa uma conversa
type ChatwootConversation struct {
	ID               int                    `json:"id"`
	ContactID        int                    `json:"contact_id"`
	InboxID          int                    `json:"inbox_id"`
	Status           string                 `json:"status"` // "open", "resolved", "pending"
	CustomAttributes map[string]interface{} `json:"custom_attributes"`
}

// ChatwootCreateConversationRequest payload para criar conversa
type ChatwootCreateConversationRequest struct {
	SourceID  string `json:"source_id"`
	ContactID int    `json:"contact_id"`
	InboxID   int    `json:"inbox_id"`
}

// ChatwootCreateConversationResponse
type ChatwootCreateConversationResponse struct {
	ID int `json:"id"`
}

// ChatwootCreateMessageRequest payload for creating a text message in a
// conversation (application/json). Anexos não cabem aqui: a API do Chatwoot
// só aceita anexo via multipart/form-data — ver
// ChatwootAdminClient.CreateMessageWithAttachment.
type ChatwootCreateMessageRequest struct {
	Content     string `json:"content"`
	MessageType string `json:"message_type"` // "incoming" or "outgoing"
	Private     bool   `json:"private"`
}

// ChatwootCreateMessageResponse response
type ChatwootCreateMessageResponse struct {
	ID int `json:"id"`
}

// ChatwootWebhook é o payload recebido do webhook da caixa de entrada API do Chatwoot.
// Os campos de mensagem ficam na raiz do payload (formato flat — sem objeto "message" aninhado).
// Ref: https://www.chatwoot.com/docs/product/channels/api/create-channel#receive-messages-using-callback-url
type ChatwootWebhook struct {
	Event string `json:"event"`

	// Campos de mensagem na raiz (message_created / message_updated)
	ID          int    `json:"id"` // ID da mensagem
	Content     string `json:"content"`
	MessageType string `json:"message_type"` // "incoming" ou "outgoing"
	ContentType string `json:"content_type"` // "text", "image", "video", "audio", "document"
	Private     bool   `json:"private"`

	Attachments []ChatwootAttachment `json:"attachments,omitempty"`

	// Objetos aninhados
	Account      ChatwootWebhookAccount      `json:"account"`
	Conversation ChatwootWebhookConversation `json:"conversation"`
	Inbox        ChatwootWebhookInbox        `json:"inbox,omitempty"`
	Sender       ChatwootWebhookSender       `json:"sender,omitempty"`

	// Para eventos de conversa (conversation_status_changed)
	ConversationStatus string `json:"conversation_status,omitempty"`
}

// ChatwootWebhookAccount é o objeto "account" no payload do webhook Chatwoot.
type ChatwootWebhookAccount struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ChatwootWebhookConversation é o objeto "conversation" no payload do webhook Chatwoot.
type ChatwootWebhookConversation struct {
	ID               int                      `json:"id"`
	InboxID          int                      `json:"inbox_id"`
	Status           string                   `json:"status"`
	CustomAttributes map[string]interface{}   `json:"custom_attributes,omitempty"`
	Meta             ChatwootConversationMeta `json:"meta,omitempty"`
	ContactInbox     ChatwootContactInboxRef  `json:"contact_inbox,omitempty"`
}

// ChatwootConversationMeta contém os metadados da conversa, incluindo o contato remetente.
type ChatwootConversationMeta struct {
	Sender ChatwootMetaSender `json:"sender,omitempty"`
}

// ChatwootMetaSender é o contato associado à conversa (tem phone_number).
type ChatwootMetaSender struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	PhoneNumber string `json:"phone_number"`
	Type        string `json:"type"`
}

// ChatwootContactInboxRef referencia a sessão do contato na caixa de entrada.
type ChatwootContactInboxRef struct {
	SourceID string `json:"source_id"`
}

// ChatwootWebhookInbox é o objeto "inbox" no payload do webhook Chatwoot.
type ChatwootWebhookInbox struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ChatwootWebhookSender é o objeto "sender" no payload do webhook Chatwoot.
type ChatwootWebhookSender struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "contact" ou "user"
}

// ChatwootAttachment representa um anexo no Chatwoot
type ChatwootAttachment struct {
	URL      string `json:"data_url"`
	FileName string `json:"filename"`
	FileType string `json:"file_type"`
}
