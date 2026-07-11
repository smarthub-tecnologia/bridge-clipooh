package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/linkkotech/bridge/internal/models"
	"go.uber.org/zap"
)

type EvolutionClient struct {
	baseURL    string
	apiKey     string
	httpClient *retryablehttp.Client
}

func NewEvolutionClient(baseURL, apiKey string) *EvolutionClient {
	client := retryablehttp.NewClient()
	client.RetryMax = 3
	client.RetryWaitMin = 1 * time.Second
	client.RetryWaitMax = 4 * time.Second
	client.Logger = nil // Disable default logger to avoid noise
	client.ErrorHandler = retryablehttp.PassthroughErrorHandler
	baseURL = strings.TrimSuffix(baseURL, "/")

	return &EvolutionClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: client,
	}
}

// CreateInstance cria uma nova instância WhatsApp
func (c *EvolutionClient) CreateInstance(ctx context.Context, req models.EvolutionCreateInstanceRequest) (*models.EvolutionCreateInstanceResponse, error) {
	url := fmt.Sprintf("%s/instance/create", c.baseURL)
	zap.L().Info("evolution_client making request", zap.String("method", "POST"), zap.String("url", url))

	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("apikey", c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		zap.L().Error("evolution create instance failed", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("evolution api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result models.EvolutionCreateInstanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// FetchInstance verifica se uma instância existe na Evolution GO
func (c *EvolutionClient) FetchInstance(ctx context.Context, instanceID string) (bool, error) {
	// Evolution GO: GET /instance/get/{instanceId}
	url := fmt.Sprintf("%s/instance/get/%s", c.baseURL, instanceID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}
	httpReq.Header.Set("apikey", c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return false, nil
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("evolution api error: status %d", resp.StatusCode)
	}

	return true, nil
}

// ConnectInstance conecta a instância e configura o webhook na Evolution GO.
// instanceToken é o token da instância (header apikey) — endpoints sem {instanceId} na URL
// identificam a instância pelo token enviado no header apikey.
// URL: POST /instance/connect
func (c *EvolutionClient) ConnectInstance(ctx context.Context, instanceToken string, req models.EvolutionConnectRequest) error {
	url := fmt.Sprintf("%s/instance/connect", c.baseURL)
	zap.L().Info("evolution_client connecting instance",
		zap.String("url", url),
		zap.String("webhook_url", req.WebhookUrl),
	)

	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("apikey", instanceToken) // Token da instância — identifica a instância no servidor

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		zap.L().Error("evolution connect instance failed", zap.Error(err))
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("evolution api connect error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// LogoutInstance encerra a sessão WhatsApp da instância na Evolution GO.
// URL: DELETE /instance/logout — identifica a instância pelo header apikey (token da instância).
// Erros são esperados quando a instância já está deslogada ou recém-criada — o chamador deve ignorá-los.
func (c *EvolutionClient) LogoutInstance(ctx context.Context, instanceToken string) error {
	url := fmt.Sprintf("%s/instance/logout", c.baseURL)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("apikey", instanceToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("evolution api logout error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}
	return nil
}

// ReconnectEvolutionInstance força a reconexão da instância no Evolution GO, gerando um novo QR Code.
// URL: POST /instance/reconnect — identifica a instância pelo header apikey (token da instância).
func (c *EvolutionClient) ReconnectEvolutionInstance(ctx context.Context, instanceToken string) error {
	url := fmt.Sprintf("%s/instance/reconnect", c.baseURL)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("apikey", instanceToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("evolution api reconnect error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}
	return nil
}

// GetInstanceQR busca o QR Code atual da instância diretamente no Evolution GO.
// URL: GET /instance/qr — identifica a instância pelo header apikey (token da instância).
// Retorna (base64String, error). Retorna ("", nil) se o QR ainda não estiver disponível (404).
func (c *EvolutionClient) GetInstanceQR(ctx context.Context, instanceToken string) (string, error) {
	url := fmt.Sprintf("%s/instance/qr", c.baseURL)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("apikey", instanceToken)

	// Disable retries for QR polling — callers handle retry logic themselves
	httpReq.Request = httpReq.Request.WithContext(ctx)

	resp, err := c.httpClient.StandardClient().Do(httpReq.Request)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "", nil // QR not yet available
	}
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("evolution api qr error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Try multiple response formats:
	// Format A: { "base64": "..." }
	// Format B: { "qrcode": { "base64": "..." } }
	// Format C: { "data": { "Qrcode": "data:image/png;base64,...", "Code": "..." }, "message": "success" }
	var formatA struct {
		Base64 string `json:"base64"`
	}
	var formatB struct {
		QRCode struct {
			Base64 string `json:"base64"`
		} `json:"qrcode"`
	}
	var formatC struct {
		Data struct {
			Qrcode string `json:"Qrcode"`
		} `json:"data"`
	}

	if err := json.Unmarshal(bodyBytes, &formatA); err == nil && formatA.Base64 != "" {
		return formatA.Base64, nil
	}
	if err := json.Unmarshal(bodyBytes, &formatB); err == nil && formatB.QRCode.Base64 != "" {
		return formatB.QRCode.Base64, nil
	}
	if err := json.Unmarshal(bodyBytes, &formatC); err == nil && formatC.Data.Qrcode != "" {
		return formatC.Data.Qrcode, nil
	}

	zap.L().Warn("GetInstanceQR: unexpected response format", zap.String("body", string(bodyBytes)))
	return "", nil
}

// SendText envia mensagem de texto via Evolution GO (POST /send/text)
// instanceToken é o token da instância (api_key salvo no DB), instanceName é o nome para roteamento
func (c *EvolutionClient) SendText(ctx context.Context, instanceToken, instanceName string, req models.EvolutionSendTextRequest) (*models.EvolutionSendResponse, error) {
	url := fmt.Sprintf("%s/send/text", c.baseURL)
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("apikey", instanceToken) // Token da instância (não a global apikey)
	httpReq.Header.Set("instance", instanceName)

	tokenPreview := instanceToken
	if len(tokenPreview) > 6 {
		tokenPreview = tokenPreview[:3] + "..." + tokenPreview[len(tokenPreview)-3:]
	}
	zap.L().Info("evolution_client sending text",
		zap.String("url", url),
		zap.String("to", req.Number),
		zap.String("instance", instanceName),
		zap.String("instance_token_preview", tokenPreview),
	)

	return c.doSendRequest(httpReq)
}

// SendMedia envia mensagem de mídia via Evolution GO (POST /send/media)
// instanceToken é o token da instância (api_key salvo no DB), instanceName é o nome para roteamento
func (c *EvolutionClient) SendMedia(ctx context.Context, instanceToken, instanceName string, req models.EvolutionSendMediaRequest) (*models.EvolutionSendResponse, error) {
	url := fmt.Sprintf("%s/send/media", c.baseURL)
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("apikey", instanceToken) // Token da instância (não a global apikey)
	httpReq.Header.Set("instance", instanceName)

	tokenPreview := instanceToken
	if len(tokenPreview) > 6 {
		tokenPreview = tokenPreview[:3] + "..." + tokenPreview[len(tokenPreview)-3:]
	}
	zap.L().Info("evolution_client sending media",
		zap.String("url", url),
		zap.String("to", req.Number),
		zap.String("instance", instanceName),
		zap.String("instance_token_preview", tokenPreview),
	)

	return c.doSendRequest(httpReq)
}

// doSendRequest executa a requisição e decodifica a resposta de envio
func (c *EvolutionClient) doSendRequest(httpReq *retryablehttp.Request) (*models.EvolutionSendResponse, error) {
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		zap.L().Error("evolution send failed",
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(bodyBytes)),
		)
		return nil, fmt.Errorf("evolution api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}
	// Evolution GO retorna um objeto: {"data": {"Info": {"ID": "..."}, ...}, "message": "success"}
	var rawResp models.EvolutionSendResponseRaw
	if err := json.NewDecoder(resp.Body).Decode(&rawResp); err != nil {
		return nil, err
	}
	return &models.EvolutionSendResponse{
		Status:    "sent",
		MessageID: rawResp.Data.Info.ID,
	}, nil
}

// EditMessage edita uma mensagem existente
// instanceToken é o token da instância (header apikey), não o instanceID
func (c *EvolutionClient) EditMessage(ctx context.Context, instanceToken string, req models.EvolutionEditRequest) (*models.EvolutionEditResponse, error) {
	// Evolution GO: POST /message/edit (sem instanceID na URL — instância via apikey header)
	url := fmt.Sprintf("%s/message/edit", c.baseURL)
	payload, _ := json.Marshal(req)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("apikey", instanceToken) // token da instância, não o global

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("evolution api error: status %d", resp.StatusCode)
	}
	var result models.EvolutionEditResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetInstanceInfo busca os dados atuais de uma instância na Evolution GO.
// Usa o global apikey (AuthAdmin). URL: GET /instance/info/{instanceId}
func (c *EvolutionClient) GetInstanceInfo(ctx context.Context, instanceID string) (*models.EvolutionInstanceInfo, error) {
	url := fmt.Sprintf("%s/instance/info/%s", c.baseURL, instanceID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("apikey", c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("instance not found in evolution: %s", instanceID)
	}
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("evolution api error: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result models.EvolutionInstanceInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode instance info response: %w", err)
	}
	return &result.Data, nil
}

// DeleteInstance remove uma instância
func (c *EvolutionClient) DeleteInstance(ctx context.Context, instanceID string) error {
	url := fmt.Sprintf("%s/instance/delete/%s", c.baseURL, instanceID)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("apikey", c.apiKey)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("evolution api error: status %d", resp.StatusCode)
	}
	return nil
}
