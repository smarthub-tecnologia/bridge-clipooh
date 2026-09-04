package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/linkkotech/bridge/internal/models"
	"go.uber.org/zap"
)

type ChatwootAdminClient struct {
	baseURL    string
	apiToken   string // Platform API token
	httpClient *retryablehttp.Client
}

func NewChatwootAdminClient(baseURL, apiToken string) *ChatwootAdminClient {
	client := retryablehttp.NewClient()
	client.RetryMax = 3
	client.RetryWaitMin = 1 * time.Second
	client.RetryWaitMax = 4 * time.Second
	client.Logger = nil // Disable default logger
	return &ChatwootAdminClient{
		baseURL:    baseURL,
		apiToken:   apiToken,
		httpClient: client,
	}
}

// CreateAccount cria uma nova conta via Platform API
func (c *ChatwootAdminClient) CreateAccount(ctx context.Context, req models.ChatwootCreateAccountRequest) (*models.ChatwootCreateAccountResponse, error) {
	url := fmt.Sprintf("%s/platform/api/v1/accounts", c.baseURL)
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		zap.L().Error("chatwoot create account failed", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatwoot api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result models.ChatwootCreateAccountResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// ValidateAccount verifica se uma conta existe via Platform API
func (c *ChatwootAdminClient) ValidateAccount(ctx context.Context, accountID int) (bool, error) {
	url := fmt.Sprintf("%s/platform/api/v1/accounts/%d", c.baseURL, accountID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return false, nil
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("chatwoot api error: status %d", resp.StatusCode)
	}

	return true, nil
}

// CreateUser cria um novo usuário via Platform API
func (c *ChatwootAdminClient) CreateUser(ctx context.Context, req models.ChatwootCreateUserRequest) (*models.ChatwootCreateUserResponse, error) {
	url := fmt.Sprintf("%s/platform/api/v1/users", c.baseURL)
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		zap.L().Error("chatwoot create user failed", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatwoot api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result models.ChatwootCreateUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// AddUserToAccount vincula um usuário a uma conta via Platform API
func (c *ChatwootAdminClient) AddUserToAccount(ctx context.Context, accountID int, req models.ChatwootAddUserToAccountRequest) error {
	url := fmt.Sprintf("%s/platform/api/v1/accounts/%d/account_users", c.baseURL, accountID)
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		zap.L().Error("chatwoot add user to account failed", zap.Error(err))
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chatwoot api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// DeleteUser apaga um usuário GLOBALMENTE via Platform API (DELETE
// /platform/api/v1/users/{id}) — enfileira um DeleteObjectJob no lado do
// Chatwoot (assíncrono, não é instantâneo). Só deve ser chamado como
// compensação de rollback quando temos certeza de que o usuário acabou de
// ser criado agora (ChatwootCreateUserResponse.Accounts veio vazio) — nunca
// para um usuário que já tinha vínculo com outras contas antes desta operação.
func (c *ChatwootAdminClient) DeleteUser(ctx context.Context, userID int) error {
	url := fmt.Sprintf("%s/platform/api/v1/users/%d", c.baseURL, userID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chatwoot api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}
	return nil
}

// RemoveAgentFromAccount desvincula um agente de UMA conta via Application
// API (DELETE /api/v1/accounts/{account_id}/agents/{id}) — mesma rota que o
// próprio painel do Chatwoot usa. Segura por desenho: o Chatwoot só apaga o
// usuário globalmente se, depois de desvinculado desta conta, ele não tiver
// mais NENHUM outro account_user (ver Api::V1::Accounts::AgentsController
// #destroy/#delete_user_record, confirmado na fonte) — nunca derruba acesso
// do agente em outras contas/workspaces.
func (c *ChatwootAdminClient) RemoveAgentFromAccount(ctx context.Context, accountID int, agentID int, accountToken string) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/agents/%d", c.baseURL, accountID, agentID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 404 é tratado como sucesso pelo caller (agente já não existia mais na
	// conta — ex.: removido manualmente no painel do Chatwoot antes).
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chatwoot api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}
	return nil
}

// RequestPasswordReset dispara o e-mail nativo de "definir senha" do Chatwoot
// via POST /auth/password (devise_token_auth, rota PÚBLICA — sem
// autenticação, mesma usada pela tela de "esqueci minha senha" do próprio
// Chatwoot). Fire-and-forget por natureza: o Chatwoot sempre responde 200
// com mensagem genérica, exista o e-mail ou não (proteção contra enumeração),
// então não há como confirmar entrega — só que a requisição foi aceita.
func (c *ChatwootAdminClient) RequestPasswordReset(ctx context.Context, email string) error {
	url := fmt.Sprintf("%s/auth/password", c.baseURL)
	payload, _ := json.Marshal(map[string]string{"email": email})
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chatwoot api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}
	return nil
}

// CreateInbox cria uma inbox do tipo WhatsApp para a conta
func (c *ChatwootAdminClient) CreateInbox(ctx context.Context, accountID int, accountToken string, req models.ChatwootCreateInboxRequest) (*models.ChatwootCreateInboxResponse, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/inboxes", c.baseURL, accountID)
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chatwoot api error: status %d", resp.StatusCode)
	}
	var result models.ChatwootCreateInboxResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateWebsiteInbox cria uma inbox do tipo Website (widget público) para a conta.
// Diferente de CreateInbox (canal "api", usado pelo combo WhatsApp), esta não tem
// webhook nem secret HMAC obrigatório — o token de identidade (hmac_token) só
// existe se a validação de identidade estiver habilitada na inbox, e por isso é
// tratado como opcional pelo caller (AdminService.CreateWidgetInbox).
// websiteURL é OBRIGATÓRIO pra Chatwoot — confirmado num teste real (a doc
// pública não deixa isso claro): sem ele a API responde 422 "Website url
// can't be blank". Só metadado exibido no painel, não afeta o funcionamento
// do widget em si.
func (c *ChatwootAdminClient) CreateWebsiteInbox(ctx context.Context, accountID int, accountToken string, name string, websiteURL string) (*models.ChatwootCreateWebsiteInboxResponse, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/inboxes", c.baseURL, accountID)
	req := models.ChatwootCreateWebsiteInboxRequest{
		Name: name,
		Channel: models.ChatwootChannelWebWidget{
			Type:       "web_widget",
			WebsiteURL: websiteURL,
		},
	}
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatwoot api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result models.ChatwootCreateWebsiteInboxResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// ListAccountWebhooks lista os webhooks de conta já cadastrados — usado pra
// checar idempotência antes de criar um novo (evita duplicar em cada
// CreateWidgetInbox subsequente na mesma conta).
func (c *ChatwootAdminClient) ListAccountWebhooks(ctx context.Context, accountID int, accountToken string) ([]models.ChatwootWebhookPayload, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/webhooks", c.baseURL, accountID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatwoot api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result models.ChatwootListWebhooksResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return result.Payload.Webhooks, nil
}

// CreateAccountWebhook cria um webhook de conta (dispara pra TODAS as inboxes
// da conta — não existe equivalente por-inbox pra Channel::WebWidget, ver
// comentário em models/chatwoot.go). O secret HMAC é gerado pelo Chatwoot
// (has_secure_token) e devolvido só nesta resposta — nunca o definimos.
func (c *ChatwootAdminClient) CreateAccountWebhook(ctx context.Context, accountID int, accountToken string, webhookURL string, subscriptions []string) (*models.ChatwootWebhookPayload, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/webhooks", c.baseURL, accountID)
	req := models.ChatwootCreateWebhookRequest{
		Webhook: models.ChatwootWebhookParams{
			URL:           webhookURL,
			Subscriptions: subscriptions,
		},
	}
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatwoot api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result models.ChatwootCreateWebhookResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result.Payload.Webhook, nil
}

// UpdateAccountWebhook atualiza as subscriptions de um webhook de conta já
// existente — usado por ensureWidgetWebhook pra migrar webhooks criados antes
// de um novo evento (ex.: conversation_typing_on/off) ser adicionado ao
// conjunto padrão em widgetWebhookSubscriptions. Não gera novo secret (o
// Chatwoot preserva o has_secure_token existente num PATCH). webhookURL é
// reenviado propositalmente — ChatwootWebhookParams.URL não tem `omitempty`,
// então omiti-lo zeraria a URL cadastrada no Chatwoot.
func (c *ChatwootAdminClient) UpdateAccountWebhook(ctx context.Context, accountID int, webhookID int, accountToken string, webhookURL string, subscriptions []string) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/webhooks/%d", c.baseURL, accountID, webhookID)
	req := models.ChatwootCreateWebhookRequest{
		Webhook: models.ChatwootWebhookParams{
			URL:           webhookURL,
			Subscriptions: subscriptions,
		},
	}
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chatwoot api error updating account webhook: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// listInboxes busca a lista de inboxes da conta no Chatwoot.
func (c *ChatwootAdminClient) listInboxes(ctx context.Context, accountID int, accountToken string) ([]models.ChatwootCreateInboxResponse, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/inboxes", c.baseURL, accountID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chatwoot api error: status %d", resp.StatusCode)
	}
	var result struct {
		Payload []models.ChatwootCreateInboxResponse `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Payload, nil
}

// GetAPIInbox retorna a primeira inbox do tipo Channel::Api da conta. É a inbox
// do bridge (o combo WhatsApp via Evolution): inboxes Channel::Whatsapp
// (ex.: Meta Cloud API) têm validação própria de source_id no Chatwoot e não
// podem ser usadas como destino da API genérica de conversas.
func (c *ChatwootAdminClient) GetAPIInbox(ctx context.Context, accountID int, accountToken string) (*models.ChatwootCreateInboxResponse, error) {
	inboxes, err := c.listInboxes(ctx, accountID, accountToken)
	if err != nil {
		return nil, err
	}
	for i := range inboxes {
		if inboxes[i].ChannelType == "Channel::Api" {
			zap.L().Info("chatwoot api inbox resolved",
				zap.Int("account_id", accountID),
				zap.Int("inbox_id", inboxes[i].ID),
				zap.String("inbox_name", inboxes[i].Name),
			)
			return &inboxes[i], nil
		}
	}
	return nil, fmt.Errorf("no Channel::Api inbox found for account %d (channel types: %s)", accountID, inboxChannelTypes(inboxes))
}

func inboxChannelTypes(inboxes []models.ChatwootCreateInboxResponse) string {
	types := make([]string, 0, len(inboxes))
	for _, inb := range inboxes {
		t := inb.ChannelType
		if t == "" {
			t = "?"
		}
		types = append(types, fmt.Sprintf("%d:%s", inb.ID, t))
	}
	return strings.Join(types, ", ")
}

// GetFirstInbox retorna o primeiro inbox da conta no Chatwoot. Mantido por
// compatibilidade, mas fluxos novos devem usar GetAPIInbox — o primeiro inbox
// pode ser de outro canal (ex.: WhatsApp/Meta) e não serve ao bridge.
func (c *ChatwootAdminClient) GetFirstInbox(ctx context.Context, accountID int, accountToken string) (*models.ChatwootCreateInboxResponse, error) {
	inboxes, err := c.listInboxes(ctx, accountID, accountToken)
	if err != nil {
		return nil, err
	}
	if len(inboxes) == 0 {
		return nil, fmt.Errorf("no inboxes found for account %d", accountID)
	}
	return &inboxes[0], nil
}

// UpdateInboxWebhook configura a URL de webhook específica de uma Inbox (Caixa de Entrada)
func (c *ChatwootAdminClient) UpdateInboxWebhook(ctx context.Context, accountID int, inboxID int, accountToken string, webhookURL string) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/inboxes/%d", c.baseURL, accountID, inboxID)
	reqBody := map[string]interface{}{
		"webhook_url": webhookURL,
	}
	payload, _ := json.Marshal(reqBody)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chatwoot api error updating inbox webhook: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// ── Custom Attribute Definitions ──────────────────────────────────────────
// Ver comentário em models/chatwoot.go sobre por que isso precisa existir
// antes de qualquer conversation.custom_attributes ser gravado de fato.

// ListCustomAttributeDefinitions lista os Custom Attribute Definitions já
// cadastrados na conta — usado pra checar idempotência antes de criar (mesmo
// padrão de ListAccountWebhooks/ensureWidgetWebhook acima).
func (c *ChatwootAdminClient) ListCustomAttributeDefinitions(ctx context.Context, accountID int, accountToken string) ([]models.ChatwootCustomAttributeDefinition, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/custom_attribute_definitions", c.baseURL, accountID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatwoot api error listing custom attribute definitions: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result []models.ChatwootCustomAttributeDefinition
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode custom attribute definitions: %w", err)
	}
	return result, nil
}

// CreateCustomAttributeDefinition cria um Custom Attribute Definition na
// conta. Não é idempotente por si só — o Chatwoot responde 422 se
// attribute_key já existir pro mesmo attribute_model; o caller
// (AdminService.ensureProfileCustomAttributes) checa via
// ListCustomAttributeDefinitions antes de chamar isto.
func (c *ChatwootAdminClient) CreateCustomAttributeDefinition(ctx context.Context, accountID int, accountToken string, def models.ChatwootCustomAttributeDefinition) (*models.ChatwootCustomAttributeDefinition, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/custom_attribute_definitions", c.baseURL, accountID)
	payload, _ := json.Marshal(def)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", accountToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatwoot api error creating custom attribute definition %q: status %d, body: %s", def.AttributeKey, resp.StatusCode, string(bodyBytes))
	}

	var result models.ChatwootCustomAttributeDefinition
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode custom attribute definition response: %w", err)
	}
	return &result, nil
}

// GetAccountAccessToken gera um token de API para a conta recém-criada
func (c *ChatwootAdminClient) GetAccountAccessToken(ctx context.Context, accountID int) (string, error) {
	url := fmt.Sprintf("%s/platform/api/v1/accounts/%d/access_tokens", c.baseURL, accountID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("chatwoot api error: status %d", resp.StatusCode)
	}
	var result models.ChatwootPlatformAccessTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.AccessToken, nil
}

// searchContact busca um contato pelo número de telefone (aceita com ou sem +).
func (c *ChatwootAdminClient) searchContact(ctx context.Context, accountID int, phoneNumber string) (*models.ChatwootContact, error) {
	digits := strings.TrimPrefix(phoneNumber, "+")
	withPlus := "+" + digits

	// Tenta busca com os dígitos e também com o + para cobrir ambos os formatos
	for _, q := range []string{digits, url.QueryEscape(withPlus)} {
		searchURL := fmt.Sprintf("%s/api/v1/accounts/%d/contacts/search?q=%s&include_contacts=true", c.baseURL, accountID, q)
		httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", searchURL, nil)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("api_access_token", c.apiToken)
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return nil, err
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}
		var searchResp models.ChatwootContactSearchResponse
		if err := json.Unmarshal(bodyBytes, &searchResp); err != nil {
			continue
		}
		zap.L().Info("chatwoot contact search result",
			zap.String("q", q),
			zap.Int("count", len(searchResp.Payload)),
			zap.String("raw", string(bodyBytes)),
		)
		for _, contact := range searchResp.Payload {
			p := contact.PhoneNumber
			if p == phoneNumber || p == withPlus || p == digits || strings.TrimPrefix(p, "+") == digits {
				return &contact, nil
			}
		}
	}

	// Fallback: filter API por phone_number exato
	return c.filterContactByPhone(ctx, accountID, phoneNumber)
}

// filterContactByPhone usa a API de filtro do Chatwoot para busca exata por phone_number.
func (c *ChatwootAdminClient) filterContactByPhone(ctx context.Context, accountID int, phoneNumber string) (*models.ChatwootContact, error) {
	digits := strings.TrimPrefix(phoneNumber, "+")
	withPlus := "+" + digits
	filterURL := fmt.Sprintf("%s/api/v1/accounts/%d/contacts/filter", c.baseURL, accountID)
	filterBody := map[string]interface{}{
		"payload": []map[string]interface{}{
			{
				"attribute_key":   "phone_number",
				"filter_operator": "equal_to",
				"values":          []string{withPlus, digits},
				"query_operator":  nil,
			},
		},
	}
	payload, _ := json.Marshal(filterBody)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", filterURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", c.apiToken)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil
	}
	var result struct {
		Payload []models.ChatwootContact `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil
	}
	if len(result.Payload) > 0 {
		return &result.Payload[0], nil
	}
	return nil, nil
}

// FindOrCreateContact busca um contato por número de telefone; se não existir, cria.
func (c *ChatwootAdminClient) FindOrCreateContact(ctx context.Context, accountID int, phoneNumber string, name string) (*models.ChatwootContact, error) {
	// 1. Busca contato existente
	contact, err := c.searchContact(ctx, accountID, phoneNumber)
	if err != nil {
		return nil, err
	}
	if contact != nil {
		return contact, nil
	}

	// 2. Não encontrou, cria novo contato
	createURL := fmt.Sprintf("%s/api/v1/accounts/%d/contacts", c.baseURL, accountID)
	contactName := name
	if contactName == "" {
		contactName = strings.TrimPrefix(phoneNumber, "+")
	}
	body := map[string]string{
		"phone_number": phoneNumber,
		"name":         contactName,
	}
	payload, _ := json.Marshal(body)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", createURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", c.apiToken)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 422 {
		// Chatwoot retorna o contato existente no body do 422 — parse direto
		body422, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var conflict models.ChatwootContactConflictResponse
		if jsonErr := json.Unmarshal(body422, &conflict); jsonErr == nil && conflict.ExistingContact.ID > 0 {
			zap.L().Info("contact already exists, recovered from 422 response",
				zap.String("phone_number", phoneNumber),
				zap.Int("contact_id", conflict.ExistingContact.ID),
				zap.Bool("recovered", true),
			)
			return &conflict.ExistingContact, nil
		}
		// Fallback: 422 body não continha o contato — re-busca
		zap.L().Warn("422 body did not contain existing_contact, falling back to search",
			zap.String("phone_number", phoneNumber),
			zap.String("body", string(body422)),
		)
		existing, searchErr := c.searchContact(ctx, accountID, phoneNumber)
		if searchErr == nil && existing != nil {
			zap.L().Info("contact recovered via fallback search after 422",
				zap.String("phone_number", phoneNumber),
				zap.Int("contact_id", existing.ID),
			)
			return existing, nil
		}
		zap.L().Error("contact already exists but could not be recovered",
			zap.String("phone_number", phoneNumber),
			zap.String("body", string(body422)),
		)
		return nil, fmt.Errorf("contact already exists but could not be found: phone=%s", phoneNumber)
	}

	// Lê body uma única vez para log e decode
	rawBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		zap.L().Error("chatwoot create contact failed",
			zap.Int("account_id", accountID),
			zap.Int("status", resp.StatusCode),
			zap.String("phone_number", phoneNumber),
			zap.String("response_body", string(rawBody)),
		)
		return nil, fmt.Errorf("failed to create contact: status %d, body: %s", resp.StatusCode, string(rawBody))
	}

	// Log do payload completo retornado — essencial para diagnosticar ID=0
	zap.L().Info("chatwoot create contact raw response",
		zap.Int("account_id", accountID),
		zap.Int("status_code", resp.StatusCode),
		zap.String("phone_number", phoneNumber),
		zap.String("response_body", string(rawBody)),
	)

	// Decode direto (flat: {"id": N, "name": "...", "phone_number": "..."})
	var newContact models.ChatwootContact
	if err := json.Unmarshal(rawBody, &newContact); err != nil {
		zap.L().Error("chatwoot create contact: failed to decode response",
			zap.String("phone_number", phoneNumber),
			zap.String("response_body", string(rawBody)),
			zap.Error(err),
		)
		return nil, fmt.Errorf("failed to decode create contact response: %w", err)
	}

	// Fallback: alguns deploys do Chatwoot retornam {"contact": {...}}
	if newContact.ID == 0 {
		var wrapped struct {
			Contact models.ChatwootContact `json:"contact"`
		}
		if jsonErr := json.Unmarshal(rawBody, &wrapped); jsonErr == nil && wrapped.Contact.ID > 0 {
			zap.L().Info("chatwoot create contact: decoded from wrapped 'contact' field",
				zap.Int("contact_id", wrapped.Contact.ID),
				zap.String("phone_number", phoneNumber),
			)
			return &wrapped.Contact, nil
		}
		zap.L().Error("chatwoot returned contact with ID=0 — ver response_body acima para diagnóstico",
			zap.String("phone_number", phoneNumber),
			zap.Int("account_id", accountID),
		)
		return nil, fmt.Errorf("chatwoot returned contact with ID=0 for phone=%s (account_id=%d)", phoneNumber, accountID)
	}

	zap.L().Info("Step 1: Contact ID resolved",
		zap.Int("contact_id", newContact.ID),
		zap.String("source", "created"),
		zap.String("phone_number", phoneNumber),
	)
	return &newContact, nil
}

// searchExistingConversation busca uma conversa open/pending para contact+inbox.
// Retorna nil (sem erro) se não encontrar nenhuma.
func (c *ChatwootAdminClient) searchExistingConversation(ctx context.Context, accountID, contactID, inboxID int) (*models.ChatwootConversation, error) {
	searchURL := fmt.Sprintf("%s/api/v1/accounts/%d/contacts/%d/conversations", c.baseURL, accountID, contactID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	rawBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		zap.L().Warn("conversation search returned non-200",
			zap.Int("status", resp.StatusCode),
			zap.Int("contact_id", contactID),
		)
		return nil, nil
	}

	// meta.channel vem como string (ex.: "Channel::Api", "Channel::Whatsapp"),
	// não como objeto {id: int} — não serve como fallback de inbox_id, que já
	// vem preenchido na raiz de cada item do payload.
	var result struct {
		Payload []struct {
			ID      int    `json:"id"`
			Status  string `json:"status"`
			InboxID int    `json:"inbox_id"`
			Meta    struct {
				Channel string `json:"channel"`
			} `json:"meta"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		zap.L().Warn("failed to decode conversation search response",
			zap.String("body", string(rawBody)),
			zap.Error(err),
		)
		return nil, nil
	}

	zap.L().Debug("conversation search result",
		zap.Int("contact_id", contactID),
		zap.Int("inbox_id", inboxID),
		zap.Int("total_conversations", len(result.Payload)),
	)

	for _, item := range result.Payload {
		if item.InboxID == inboxID && (item.Status == "open" || item.Status == "pending") {
			zap.L().Info("Step 1: Found existing conversation",
				zap.Int("conversation_id", item.ID),
				zap.String("status", item.Status),
				zap.Int("contact_id", contactID),
				zap.Int("inbox_id", inboxID),
			)
			return &models.ChatwootConversation{
				ID:      item.ID,
				InboxID: inboxID,
				Status:  item.Status,
			}, nil
		}
	}
	return nil, nil
}

// FindOrCreateConversation busca uma conversa open/pending para contact+inbox;
// se não existir, cria uma nova.
//
// sourceID é o source_id do contact_inbox a enviar ao Chatwoot. Quando a inbox
// de destino é Channel::Whatsapp, o Chatwoot valida o formato e só aceita o
// wa_id do contato (dígitos, sem "+", até 15) ou o formato LID
// "[A-Z]{2}.(ENT.)?[A-Za-z0-9]{1,128}" — enviar um source_id arbitrário
// (ex.: "whatsapp-<contactId>") resulta em 422 "invalid source id for whatsapp
// inbox" e derruba a entrega da mensagem. Por isso o caller deve passar os
// dígitos do telefone resolvido (ver handleIncomingMessage). Em inboxes
// Channel::Api qualquer string é aceita; os dígitos continuam servindo como
// source_id estável por contato.
func (c *ChatwootAdminClient) FindOrCreateConversation(ctx context.Context, accountID int, inboxID int, contactID int, sourceID string) (*models.ChatwootConversation, error) {
	if contactID == 0 {
		return nil, fmt.Errorf("FindOrCreateConversation: contactID must be non-zero (account_id=%d, inbox_id=%d)", accountID, inboxID)
	}

	existing, err := c.searchExistingConversation(ctx, accountID, contactID, inboxID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		zap.L().Info("Step 2: Reusing existing conversation",
			zap.Int("conversation_id", existing.ID),
			zap.String("status", existing.Status),
			zap.Int("contact_id", contactID),
			zap.Int("inbox_id", inboxID),
		)
		return existing, nil
	}

	// Normaliza o source_id: wa_id nunca leva "+" e o Chatwoot rejeita com 422
	// se vier com ele em inbox Channel::Whatsapp.
	sourceID = strings.TrimPrefix(sourceID, "+")
	if sourceID == "" {
		// Fallback apenas para compatibilidade com a convenção antiga usada em
		// inboxes Channel::Api (qualquer string é válida lá). Em Channel::Whatsapp
		// isto ainda seria rejeitado — mas source_id vazio aqui indica ausência
		// de telefone, caso que já não deveria chegar até este ponto.
		sourceID = fmt.Sprintf("whatsapp-%d", contactID)
	}

	// Nenhuma conversa open/pending — cria nova.
	createURL := fmt.Sprintf("%s/api/v1/accounts/%d/conversations", c.baseURL, accountID)
	reqBody := models.ChatwootCreateConversationRequest{
		SourceID:  sourceID,
		ContactID: contactID,
		InboxID:   inboxID,
	}
	payload, _ := json.Marshal(reqBody)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", createURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		zap.L().Error("chatwoot create conversation failed",
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(bodyBytes)),
			zap.Int("account_id", accountID),
			zap.Int("inbox_id", inboxID),
			zap.Int("contact_id", contactID),
		)
		return nil, fmt.Errorf("failed to create conversation: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var convResp models.ChatwootCreateConversationResponse
	if err := json.NewDecoder(resp.Body).Decode(&convResp); err != nil {
		return nil, err
	}
	zap.L().Info("Step 2: Created new conversation",
		zap.Int("conversation_id", convResp.ID),
		zap.Int("contact_id", contactID),
		zap.Int("inbox_id", inboxID),
	)
	return &models.ChatwootConversation{ID: convResp.ID, ContactID: contactID, InboxID: inboxID, Status: "open"}, nil
}

// SetCustomAttribute define um atributo customizado na conversa
func (c *ChatwootAdminClient) SetCustomAttribute(ctx context.Context, accountID int, conversationID int, attributeName string, value string) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/custom_attributes", c.baseURL, accountID, conversationID)
	body := map[string]interface{}{
		"custom_attributes": map[string]string{
			attributeName: value,
		},
	}
	payload, _ := json.Marshal(body)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", c.apiToken)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// CreateMessage creates a message in a conversation
func (c *ChatwootAdminClient) CreateMessage(ctx context.Context, accountID int, conversationID int, req models.ChatwootCreateMessageRequest) (*models.ChatwootCreateMessageResponse, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/messages", c.baseURL, accountID, conversationID)
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chatwoot api error: status %d", resp.StatusCode)
	}

	var result models.ChatwootCreateMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// quoteEscaper replica o escapamento usado internamente por
// mime/multipart.Writer.CreateFormFile (não exportado) para o valor de
// filename no header Content-Disposition.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// CreateMessageWithAttachment cria uma mensagem com um arquivo anexado. A API
// pública do Chatwoot só aceita anexos via multipart/form-data no campo
// attachments[] — não existe um endpoint JSON equivalente a data_url, então
// isto não pode reaproveitar CreateMessage (que envia application/json).
func (c *ChatwootAdminClient) CreateMessageWithAttachment(ctx context.Context, accountID, conversationID int, content string, fileBytes []byte, fileName, mimeType string) (*models.ChatwootCreateMessageResponse, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/messages", c.baseURL, accountID, conversationID)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("message_type", "incoming"); err != nil {
		return nil, err
	}
	if content != "" {
		if err := writer.WriteField("content", content); err != nil {
			return nil, err
		}
	}
	// writer.CreateFormFile fixaria Content-Type: application/octet-stream na
	// parte do arquivo (é hardcoded na stdlib) — o Chatwoot deriva o
	// file_type do anexo (image/video/audio/file) desse Content-Type via
	// ActiveStorage, não da extensão do nome do arquivo. Sem o mimetype real
	// aqui, toda mídia (imagem incluída) cai no cartão genérico de arquivo em
	// vez de renderizar a prévia inline.
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="attachments[]"; filename="%s"`, quoteEscaper.Replace(fileName)))
	partHeader.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(fileBytes); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, body.Bytes())
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpReq.Header.Set("api_access_token", c.apiToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		rawBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chatwoot api error uploading attachment: status %d, body: %s", resp.StatusCode, string(rawBody))
	}

	var result models.ChatwootCreateMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetConversationCustomAttributes busca os atributos customizados de uma conversa
func (c *ChatwootAdminClient) GetConversationCustomAttributes(ctx context.Context, accountID int, conversationID int) (map[string]interface{}, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d", c.baseURL, accountID, conversationID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("api_access_token", c.apiToken)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chatwoot api error: status %d", resp.StatusCode)
	}
	var conversation struct {
		CustomAttributes map[string]interface{} `json:"custom_attributes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&conversation); err != nil {
		return nil, err
	}
	return conversation.CustomAttributes, nil
}
