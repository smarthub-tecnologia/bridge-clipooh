package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/config"
	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/observability"
	"github.com/linkkotech/bridge/internal/repository"
)

type BridgeService struct {
	instanceRepo        repository.InstanceRepository
	inboxRepo           *repository.InboxRepository
	evolutionClient     *EvolutionClient
	mediaService        *MediaService
	cacheService        *CacheService
	metrics             *observability.MetricsCollector
	chatwoot            config.ChatwootConfig
	reconnectInProgress sync.Map
}

func NewBridgeService(
	instanceRepo repository.InstanceRepository,
	inboxRepo *repository.InboxRepository,
	evolutionClient *EvolutionClient,
	mediaService *MediaService,
	chatwootCfg config.ChatwootConfig,
) *BridgeService {
	return &BridgeService{
		instanceRepo:    instanceRepo,
		inboxRepo:       inboxRepo,
		evolutionClient: evolutionClient,
		mediaService:    mediaService,
		chatwoot:        chatwootCfg,
	}
}

func (b *BridgeService) SetCacheService(cache *CacheService) {
	b.cacheService = cache
}

func (b *BridgeService) SetMetrics(m *observability.MetricsCollector) {
	b.metrics = m
}

// ValidateChatwootWebhookSecret valida a assinatura HMAC-SHA256 do webhook Chatwoot
// contra o secret único fixo da conta (CHATWOOT_WEBHOOK_SECRET) — não há mais
// lookup por tenant, só existe uma conta Chatwoot.
func (b *BridgeService) ValidateChatwootWebhookSecret(r *nethttp.Request) bool {
	secret := strings.TrimSpace(b.chatwoot.WebhookSecret)
	// Se não há segredo configurado, aceita qualquer request
	if secret == "" {
		zap.L().Warn("CHATWOOT_WEBHOOK_SECRET not set, accepting request")
		return true
	}

	// Log de identidade do segredo (nunca loga o segredo completo)
	secretPreview := secret
	if len(secretPreview) >= 6 {
		secretPreview = secretPreview[:3] + "..." + secretPreview[len(secretPreview)-3:]
	}
	zap.L().Info("chatwoot HMAC: secret identity",
		zap.Int("secret_len", len(secret)),
		zap.String("secret_preview", secretPreview),
	)

	// Chatwoot assina o body com HMAC-SHA256 e envia X-Chatwoot-Signature: sha256=<hex>
	sigHeader := r.Header.Get("X-Chatwoot-Signature")
	if sigHeader == "" {
		zap.L().Warn("missing X-Chatwoot-Signature header")
		return false
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	// Restitui o body para leitura posterior no handler
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	// Log de payload
	bodyPreview := string(body)
	if len(bodyPreview) > 50 {
		bodyPreview = bodyPreview[:50]
	}
	zap.L().Info("chatwoot HMAC: payload info",
		zap.Int("body_len", len(body)),
		zap.String("body_preview", bodyPreview),
	)

	// Chatwoot assina "timestamp.body" quando X-Chatwoot-Timestamp está presente
	// Ref: lib/webhooks/trigger.rb — "#{ts}.#{body}"
	ts := r.Header.Get("X-Chatwoot-Timestamp")
	mac := hmac.New(sha256.New, []byte(secret))
	if ts != "" {
		mac.Write([]byte(ts + "." + string(body)))
	} else {
		mac.Write(body)
	}
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	ok := hmac.Equal([]byte(sigHeader), []byte(expected))
	if !ok {
		recvPreview := sigHeader
		if len(recvPreview) > 15 {
			recvPreview = recvPreview[:15] + "..."
		}
		expPreview := expected
		if len(expPreview) > 15 {
			expPreview = expPreview[:15] + "..."
		}
		zap.L().Error("chatwoot HMAC mismatch",
			zap.String("received_prefix", recvPreview),
			zap.String("expected_prefix", expPreview),
			zap.Int("secret_len", len(secret)),
			zap.Int("body_len", len(body)),
		)
		// Bypass de emergência: remove após confirmar funcionamento
		if os.Getenv("SKIP_HMAC_VALIDATION") == "true" {
			zap.L().Warn("SKIP_HMAC_VALIDATION=true: bypassing HMAC check (REMOVE IN PRODUCTION)")
			return true
		}
	}
	return ok
}

// HandleEvolutionWebhook processa eventos recebidos da Evolution
func (b *BridgeService) HandleEvolutionWebhook(ctx context.Context, webhook models.EvolutionWebhook) error {
	logger := zap.L().With(
		zap.String("event", webhook.Event),
		zap.String("instance", webhook.InstanceName),
	)

	switch webhook.Event {

	// MESSAGE
	case "Message", "SendMessage":
		return b.handleIncomingMessage(ctx, webhook, logger)

	// CONNECTION: sucesso de conexão (open)
	case "Connected", "PairSuccess", "QRSuccess":
		return b.handleConnectionUpdate(ctx, webhook, logger)

	// CONNECTION: desconexão
	case "LoggedOut":
		return b.handleLogout(ctx, webhook, logger)

	// CONNECTION: o WebSocket do WhatsApp caiu sem logout explicito — Evolution GO envia "Disconnected"
	case "Disconnected":
		return b.handleDisconnected(ctx, webhook, logger)

	// INSTANCE: deleção
	case "DeleteInstance", "RemoveInstance":
		return b.handleInstanceDeleted(ctx, webhook, logger)

	// QRCODE
	case "QRCode":
		return b.handleQRCodeUpdated(ctx, webhook, logger)
	case "QRTimeout":
		return b.handleQRTimeout(ctx, webhook, logger)

	// CONNECTION: sync offline (informativo, sem alteração de estado)
	case "OfflineSyncCompleted":
		logger.Info("offline sync completed", zap.String("instance", webhook.InstanceName))
		return nil

	// READ_RECEIPT
	case "Read", "ReadSelf", "Delivered":
		logger.Debug("evento mapeado recebido (No-op atual)", zap.String("event", webhook.Event))
		return nil

	// PRESENCE
	case "Presence":
		logger.Debug("evento mapeado recebido (No-op atual)", zap.String("event", webhook.Event))
		return nil

	// HISTORY_SYNC
	case "HistorySync":
		logger.Debug("evento mapeado recebido (No-op atual)", zap.String("event", webhook.Event))
		return nil

	// CHAT_PRESENCE
	case "ChatPresence", "Archive":
		logger.Debug("evento mapeado recebido (No-op atual)", zap.String("event", webhook.Event))
		return nil

	// CALL
	case "CallOffer", "CallRelayLatency", "CallTerminate":
		logger.Debug("evento mapeado recebido (No-op atual)", zap.String("event", webhook.Event))
		return nil

	// LABEL
	case "LabelEdit", "LabelAssociationChat", "LabelAssociationMessage":
		logger.Debug("evento mapeado recebido (No-op atual)", zap.String("event", webhook.Event))
		return nil

	// CONTACT
	case "Contact", "PushName":
		logger.Debug("evento mapeado recebido (No-op atual)", zap.String("event", webhook.Event))
		return nil

	// GROUP
	case "GroupInfo", "JoinedGroup":
		logger.Debug("evento mapeado recebido (No-op atual)", zap.String("event", webhook.Event))
		return nil

	// NEWSLETTER
	case "NewsletterJoin", "NewsletterLeave":
		logger.Debug("evento mapeado recebido (No-op atual)", zap.String("event", webhook.Event))
		return nil

	default:
		logger.Warn("evento DESCONHECIDO recebido",
			zap.String("event", webhook.Event),
			zap.String("instance", webhook.InstanceName),
			zap.String("raw_data", string(webhook.Data)),
		)
		return nil
	}
}

func (b *BridgeService) handleIncomingMessage(ctx context.Context, webhook models.EvolutionWebhook, logger *zap.Logger) (retErr error) {
	var data models.EvolutionMessageData
	if err := json.Unmarshal(webhook.Data, &data); err != nil {
		logger.Error("failed to decode message data", zap.Error(err))
		return nil // Ignore malformed data to avoid retries
	}
	info := data.Info
	logger = logger.With(zap.String("message_id", info.ID))

	// Ignora mensagens enviadas pelo próprio bot
	if info.IsFromMe {
		return nil
	}

	// Deduplicação por message_id — protege contra webhooks duplicados e race conditions.
	// O defer só marca como processado em caso de sucesso (retErr == nil).
	if b.cacheService != nil && info.ID != "" {
		already, err := b.cacheService.IsProcessed(ctx, info.ID)
		if err != nil {
			zap.L().Warn("dedup check failed, proceeding without dedup",
				zap.String("message_id", info.ID),
				zap.Error(err),
			)
		} else if already {
			zap.L().Info("duplicate message, skipping",
				zap.String("message_id", info.ID),
				zap.String("instance", webhook.InstanceName),
			)
			return nil
		}
		defer func() {
			if retErr == nil {
				b.cacheService.MarkProcessed(ctx, info.ID)
			}
		}()
	}

	if b.metrics != nil {
		b.metrics.InboundTotal.Add(1)
		defer func() {
			if retErr != nil {
				b.metrics.InboundError.Add(1)
			} else {
				b.metrics.InboundSuccess.Add(1)
			}
		}()
	}

	logger = logger.With(
		zap.String("chat", info.Chat),
		zap.String("type", info.Type),
	)

	// Resolve a instância diretamente pelo nome (?instance= na URL do webhook) —
	// não há mais tenant para desambiguar; cada instância Evolution existe
	// isoladamente e pertence sempre à única conta Chatwoot da Cartão Pro.
	instance, err := b.instanceRepo.FindByInstanceName(ctx, webhook.InstanceName)
	if err != nil {
		logger.Error("instance not found by name", zap.Error(err))
		return fmt.Errorf("instance %s not found: %w", webhook.InstanceName, err)
	}

	// VÍNCULO OBRIGATÓRIO — roteamento por instância: cada instância precisa ter
	// uma inbox Chatwoot vinculada (cadastrada no dashboard via create/update do
	// vínculo). Sem vínculo a mensagem NÃO é entregue: com várias caixas na
	// conta, cair numa inbox "default" poderia misturar atendimentos de números
	// diferentes. A resolução deixa de usar a tabela global chatwoot_inboxes.
	inboxID := 0
	if instance.ChatwootInboxID == nil {
		logger.Error("instance has no linked chatwoot inbox — message NOT delivered",
			zap.String("instance", webhook.InstanceName),
			zap.String("hint", "vincule uma inbox Chatwoot no dashboard (PUT /api/v1/admin/instances/{name}/inbox)"),
		)
		return fmt.Errorf("instance %s has no linked chatwoot inbox; message not delivered — link one before receiving messages", webhook.InstanceName)
	}
	inboxID = *instance.ChatwootInboxID
	logger.Info("inbound routed to linked chatwoot inbox",
		zap.Int("inbox_id", inboxID),
	)

	chatwootClient := NewChatwootAdminClient(b.chatwoot.InternalURL, b.chatwoot.APIToken)
	accountID := b.chatwoot.AccountID

	// CORREÇÃO LID: quando o Chat é um LID (@lid), o Sender contém o JID real do contato
	phoneRaw := info.Chat
	if strings.HasSuffix(info.Chat, "@lid") && info.Sender != "" && !strings.HasSuffix(info.Sender, "@lid") {
		phoneRaw = info.Sender
	}
	phoneNumber := strings.TrimSuffix(phoneRaw, "@s.whatsapp.net")
	phoneNumber = strings.TrimSuffix(phoneNumber, "@c.us")
	phoneNumber = strings.TrimSuffix(phoneNumber, "@lid")
	if len(phoneNumber) > 0 && phoneNumber[0] != '+' {
		phoneNumber = "+" + phoneNumber
	}
	// sourceID do contact_inbox no Chatwoot: wa_id do contato (dígitos sem "+").
	// Inbox Channel::Whatsapp só aceita este formato (ou o padrão LID) como
	// source_id — qualquer outra string (ex.: "whatsapp-<id>") retorna 422.
	phoneSourceID := strings.TrimPrefix(phoneNumber, "+")

	logger.Info("processing incoming message",
		zap.Int("account_id", accountID),
		zap.String("phone_number", phoneNumber),
		zap.String("source_id", phoneSourceID),
	)

	// 3. Encontra ou cria contato
	contact, err := chatwootClient.FindOrCreateContact(ctx, accountID, phoneNumber, info.PushName)
	if err != nil {
		logger.Error("Step 1 FAILED: could not find/create contact", zap.Error(err))
		return err
	}
	if contact.ID == 0 {
		logger.Error("Step 1 FAILED: contact returned with ID=0",
			zap.String("phone_number", phoneNumber),
		)
		return fmt.Errorf("contact ID is zero for phone=%s, aborting message flow", phoneNumber)
	}
	logger.Info("Step 1: Contact ID resolved", zap.Int("contact_id", contact.ID))

	// 4. Encontra ou cria conversa na inbox Chatwoot vinculada à instância
	// (resolvida no passo 2 — vínculo obrigatório, roteamento por instância).
	conversation, err := chatwootClient.FindOrCreateConversation(ctx, accountID, inboxID, contact.ID, phoneSourceID)
	if err != nil {
		logger.Error("failed to find/create conversation", zap.Error(err))
		return err
	}

	// 5. Armazena custom attributes para rastreamento
	chatwootClient.SetCustomAttribute(ctx, accountID, conversation.ID, "whatsapp_message_id", info.ID)
	chatwootClient.SetCustomAttribute(ctx, accountID, conversation.ID, "phone_number", phoneNumber)
	chatwootClient.SetCustomAttribute(ctx, accountID, conversation.ID, "evolution_instance", webhook.InstanceName)

	// 6. Prepara mensagem
	msg := data.Message
	var messageReq models.ChatwootCreateMessageRequest
	messageReq.MessageType = "incoming"

	switch info.Type {
	case "Conversation", "ExtendedTextMessage", "text", "extendedText", "conversation":
		messageReq.Content = msg.Text()
		if messageReq.Content == "" {
			logger.Warn("empty text message, skipping")
			return nil
		}

	case "ImageMessage", "VideoMessage", "AudioMessage", "DocumentMessage",
		"imageMessage", "videoMessage", "audioMessage", "documentMessage":
		media := msg.MediaContent()
		if media != nil {
			filePath, err := b.mediaService.DownloadFile(ctx, media.URL)
			if err != nil {
				logger.Error("failed to download media", zap.Error(err))
				return err
			}
			defer b.mediaService.Cleanup(filePath)

			fileBytes, err := b.mediaService.ReadFile(filePath)
			if err != nil {
				return err
			}
			dataURL, err := chatwootClient.UploadAttachment(ctx, accountID, fileBytes, media.FileName, media.Mimetype)
			if err != nil {
				return err
			}
			messageReq.Attachments = []models.ChatwootAttachmentRequest{{
				FileName: media.FileName,
				FileType: media.Mimetype,
				DataURL:  dataURL,
			}}
			if media.Caption != "" {
				messageReq.Content = media.Caption
			}
		}

	default:
		logger.Warn("unsupported message type", zap.String("type", info.Type))
		return nil
	}

	// 7. Envia mensagem para o Chatwoot (com retry único em caso de 404 por ID obsoleto)
	conversationID := conversation.ID
	_, sendErr := chatwootClient.CreateMessage(ctx, accountID, conversationID, messageReq)
	if sendErr != nil && strings.Contains(sendErr.Error(), "status 404") {
		logger.Warn("[Recovery] Stale ID detected. Clearing cache and re-syncing contact...",
			zap.String("phone_number", phoneNumber),
			zap.Int("stale_contact_id", contact.ID),
			zap.Int("stale_conversation_id", conversationID),
		)
		// Re-fetch contact fresh (invalida qualquer ID em memória)
		freshContact, recovErr := chatwootClient.FindOrCreateContact(ctx, accountID, phoneNumber, info.PushName)
		if recovErr != nil {
			logger.Error("[Recovery] FAILED: could not re-sync contact", zap.Error(recovErr))
			return recovErr
		}
		if freshContact.ID == 0 {
			return fmt.Errorf("[Recovery] re-synced contact has ID=0 for phone=%s", phoneNumber)
		}
		// Inbox NÃO é re-resolvida no recovery: com vínculo obrigatório ela vem do
		// binding da instância (inboxID do passo 2). Se a inbox vinculada tiver
		// sumido no Chatwoot, o recovery não deve cair numa inbox "default".
		// Re-fetch conversa com IDs frescos
		freshConv, recovErr := chatwootClient.FindOrCreateConversation(ctx, accountID, inboxID, freshContact.ID, phoneSourceID)
		if recovErr != nil {
			logger.Error("[Recovery] FAILED: could not re-sync conversation", zap.Error(recovErr))
			return recovErr
		}
		logger.Info("[Recovery] IDs re-synced, retrying message send",
			zap.Int("fresh_contact_id", freshContact.ID),
			zap.Int("fresh_conversation_id", freshConv.ID),
		)
		chatwootClient.SetCustomAttribute(ctx, accountID, freshConv.ID, "whatsapp_message_id", info.ID)
		chatwootClient.SetCustomAttribute(ctx, accountID, freshConv.ID, "phone_number", phoneNumber)
		chatwootClient.SetCustomAttribute(ctx, accountID, freshConv.ID, "evolution_instance", webhook.InstanceName)
		_, sendErr = chatwootClient.CreateMessage(ctx, accountID, freshConv.ID, messageReq)
		contact = freshContact
		conversationID = freshConv.ID
	}
	if sendErr != nil {
		logger.Error("failed to create message in chatwoot", zap.Error(sendErr))
		return sendErr
	}

	logger.Info("message forwarded to chatwoot",
		zap.Int("contact_id", contact.ID),
		zap.Int("conversation_id", conversationID),
	)

	return nil
}

func (b *BridgeService) handleEditedMessage(ctx context.Context, webhook models.EvolutionWebhook, logger *zap.Logger) error {
	// Edição: requer mapeamento evolution_message_id -> chatwoot_message_id (não implementado)
	logger.Info("edited message received (mapping table not implemented yet)")
	return nil
}

func (b *BridgeService) handleConnectionUpdate(ctx context.Context, webhook models.EvolutionWebhook, logger *zap.Logger) error {
	var payload struct {
		State        string `json:"state"`
		StatusReason int    `json:"statusReason"`
		Wuid         string `json:"wuid"`
		Jid          string `json:"jid"`
		PushName     string `json:"pushName"`
		Status       string `json:"status"`
	}
	if err := json.Unmarshal(webhook.Data, &payload); err != nil {
		logger.Error("failed to decode connection.update payload", zap.Error(err))
		return nil
	}

	// Evento "Connected" da Evolution GO: { jid, pushName, status: "open" }
	// O jid vem no formato "5511999998888:98@s.whatsapp.net"
	if payload.Jid != "" && payload.Status == "open" {
		jidClean := strings.SplitN(payload.Jid, "@", 2)[0] // remove @s.whatsapp.net
		jidClean = strings.SplitN(jidClean, ":", 2)[0]     // remove sufixo :XX do device
		phone := "+" + jidClean

		logger.Info("Connected event received", zap.String("phone", phone), zap.String("push_name", payload.PushName))

		if err := b.instanceRepo.UpdateConnectionState(ctx, webhook.InstanceName, "open", &phone); err != nil {
			logger.Error("Connected: failed to update connection state", zap.String("instance", webhook.InstanceName), zap.Error(err))
			return nil
		}

		if b.cacheService != nil {
			b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
			b.cacheService.ClearInstanceRateLimits(ctx, webhook.InstanceName)
		}

		b.notifyConnectionEvent(logger, "connected", map[string]interface{}{
			"instance":  webhook.InstanceName,
			"phone":     phone,
			"push_name": payload.PushName,
			"jid":       payload.Jid,
		})

		return nil
	}

	// Fallback: Evolution GO mais antigo envia state:"open" + wuid em vez de jid
	if payload.State == "open" && payload.Wuid != "" {
		phone := "+" + strings.Replace(payload.Wuid, "@s.whatsapp.net", "", 1)

		if err := b.instanceRepo.UpdateConnectionState(ctx, webhook.InstanceName, "open", &phone); err != nil {
			logger.Error("failed to update instance state to open (wuid fallback)", zap.Error(err))
			return nil
		}

		if b.cacheService != nil {
			b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
			b.cacheService.ClearInstanceRateLimits(ctx, webhook.InstanceName)
		}
		b.notifyConnectionEvent(logger, "connected", map[string]interface{}{
			"instance":  webhook.InstanceName,
			"phone":     phone,
			"push_name": payload.PushName,
			"wuid":      payload.Wuid,
		})

	} else {
		logger.Warn("handleConnectionUpdate: payload sem jid nem wuid reconhecível, ignorando",
			zap.String("state", payload.State),
			zap.String("jid", payload.Jid),
			zap.String("wuid", payload.Wuid),
		)
	}

	return nil
}

func (b *BridgeService) handleQRCodeUpdated(ctx context.Context, webhook models.EvolutionWebhook, logger *zap.Logger) error {
	var payload struct {
		Code     string `json:"code"`
		Count    int    `json:"count"`
		MaxCount int    `json:"maxCount"`
		QRCode   string `json:"qrcode"`
	}
	if err := json.Unmarshal(webhook.Data, &payload); err != nil {
		logger.Error("failed to decode qrcode.updated payload", zap.Error(err))
		return nil
	}

	if payload.QRCode != "" && b.cacheService != nil {
		b.cacheService.SaveQRCode(ctx, webhook.InstanceName, payload.QRCode, 60*time.Second)
	}

	err := b.instanceRepo.UpdateConnectionState(ctx, webhook.InstanceName, "connecting", nil)
	if err != nil {
		logger.Warn("instance not found or failed to update to connecting", zap.Error(err))
	}

	return nil
}

func (b *BridgeService) handleQRTimeout(ctx context.Context, webhook models.EvolutionWebhook, logger *zap.Logger) error {
	err := b.instanceRepo.UpdateConnectionState(ctx, webhook.InstanceName, "close", nil)
	if err != nil {
		logger.Warn("failed to update instance state to disconnected for qr timeout", zap.Error(err))
	}

	if b.cacheService != nil {
		b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
	}

	var qrData struct {
		QRCount     int  `json:"qrcount"`
		MaxCount    int  `json:"maxCount"`
		ForceLogout bool `json:"forceLogout"`
	}
	json.Unmarshal(webhook.Data, &qrData)

	if qrData.QRCount >= qrData.MaxCount || qrData.ForceLogout {
		instance, err := b.instanceRepo.FindByInstanceName(ctx, webhook.InstanceName)
		if err != nil || instance == nil || instance.APIKey == nil {
			logger.Warn("QR limit reached: could not fetch instance token for auto-reconnect", zap.Error(err))
		} else if instance.Status == "connected" {
			logger.Info("QR limit reached but instance already connected, skipping reconnect",
				zap.String("instance", webhook.InstanceName))
		} else {
			if _, loaded := b.reconnectInProgress.LoadOrStore(webhook.InstanceName, true); loaded {
				logger.Info("QR limit reached: reconnect already in progress, skipping",
					zap.String("instance", webhook.InstanceName))
			} else {
				instanceToken := *instance.APIKey
				instanceName := webhook.InstanceName
				go func() {
					defer b.reconnectInProgress.Delete(instanceName)
					time.Sleep(2 * time.Second)
					webhookURL := fmt.Sprintf("%s/webhook/evolution?instance=%s",
						os.ExpandEnv("${WEBHOOK_BASE_URL}"), instanceName)
					if secret := os.Getenv("WEBHOOK_SECRET_EVOLUTION"); secret != "" {
						webhookURL += "&token=" + secret
					}
					connectReq := models.EvolutionConnectRequest{
						Immediate:  false,
						WebhookUrl: webhookURL,
						Subscribe:  []string{"MESSAGE", "CONNECTION", "QRCODE"},
					}
					if err := b.evolutionClient.ConnectInstance(context.Background(), instanceToken, connectReq); err != nil {
						logger.Warn("QR limit reached: auto-connect failed", zap.Error(err))
					} else {
						logger.Info("QR limit reached: auto-connect triggered (Immediate=false)")
					}
				}()
			}
		}
	}

	logger.Info("QRTimeout handled: instance disconnected and qr cache cleared")
	return nil
}

func (b *BridgeService) handleLogout(ctx context.Context, webhook models.EvolutionWebhook, logger *zap.Logger) error {
	if err := b.instanceRepo.LogoutInstance(ctx, webhook.InstanceName); err != nil {
		logger.Error("failed to logout instance in db", zap.Error(err))
		return nil
	}

	if b.cacheService != nil {
		b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
	}

	b.notifyConnectionEvent(logger, "logged_out", map[string]interface{}{
		"instance": webhook.InstanceName,
		"reason":   "logout",
	})

	return nil
}

func (b *BridgeService) handleInstanceDeleted(ctx context.Context, webhook models.EvolutionWebhook, logger *zap.Logger) error {
	if err := b.instanceRepo.DeleteInstance(ctx, webhook.InstanceName); err != nil {
		logger.Error("failed to mark instance as deleted", zap.Error(err))
		return nil
	}

	if b.cacheService != nil {
		b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
		b.cacheService.ClearInstanceRateLimits(ctx, webhook.InstanceName)
	}

	return nil
}

// notifyConnectionEvent dispara um webhook de saída (outbound) para
// CONNECTION_WEBHOOK_URL sempre que o estado de conexão de uma instância muda
// (connected/disconnected/logged_out). Permite a um consumidor externo (ex.:
// workflow n8n) saber na hora que o WhatsApp conectou e, por exemplo, parar de
// gerar QR codes. Fire-and-forget: roda em goroutine com timeout de 5s e nunca
// bloqueia o processamento do webhook recebido da Evolution.
// Opcional: CONNECTION_WEBHOOK_TOKEN é enviado no header X-Access-Token.
func (b *BridgeService) notifyConnectionEvent(logger *zap.Logger, event string, payload map[string]interface{}) {
	url := strings.TrimSpace(os.Getenv("CONNECTION_WEBHOOK_URL"))
	if url == "" {
		logger.Debug("connection webhook not configured (CONNECTION_WEBHOOK_URL), skipping outbound event",
			zap.String("event", event))
		return
	}
	token := strings.TrimSpace(os.Getenv("CONNECTION_WEBHOOK_TOKEN"))
	payload["event"] = event
	payload["ts"] = time.Now().Unix()

	body, err := json.Marshal(payload)
	if err != nil {
		logger.Warn("connection webhook: failed to marshal payload", zap.String("event", event), zap.Error(err))
		return
	}

	go func() {
		req, err := nethttp.NewRequest(nethttp.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			logger.Warn("connection webhook: failed to create request", zap.String("event", event), zap.String("url", url), zap.Error(err))
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Access-Token", token)
		}
		client := &nethttp.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			logger.Warn("connection webhook: delivery failed", zap.String("event", event), zap.String("url", url), zap.Error(err))
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode >= 300 {
			logger.Warn("connection webhook: non-2xx response", zap.String("event", event), zap.Int("status", resp.StatusCode))
			return
		}
		logger.Info("connection webhook delivered", zap.String("event", event), zap.Int("status", resp.StatusCode))
	}()
}

// handleDisconnected processa o evento "Disconnected" da Evolution GO: o stream
// do WhatsApp caiu (WebSocket fechado), mas a sessão ainda está pareada. Marca a
// instância como disconnected no banco e notifica via outbound webhook.
func (b *BridgeService) handleDisconnected(ctx context.Context, webhook models.EvolutionWebhook, logger *zap.Logger) error {
	if err := b.instanceRepo.UpdateConnectionState(ctx, webhook.InstanceName, "close", nil); err != nil {
		logger.Warn("Disconnected: failed to update instance state", zap.Error(err))
	}

	if b.cacheService != nil {
		b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
	}

	b.notifyConnectionEvent(logger, "disconnected", map[string]interface{}{
		"instance": webhook.InstanceName,
		"reason":   "stream_disconnected",
	})
	return nil
}

// HandleChatwootWebhook processa eventos do Chatwoot (respostas de agentes, edições)
func (b *BridgeService) HandleChatwootWebhook(ctx context.Context, webhook models.ChatwootWebhook) error {
	logger := zap.L().With(
		zap.String("event", webhook.Event),
		zap.Int("account_id", webhook.Account.ID),
		zap.Int("conversation_id", webhook.Conversation.ID),
	)

	logger.Info("Processing Chatwoot event",
		zap.String("message_type", webhook.MessageType),
		zap.String("content_type", webhook.ContentType),
		zap.Bool("private", webhook.Private),
	)

	switch webhook.Event {
	case "message_created":
		if webhook.MessageType == "outgoing" && !webhook.Private {
			return b.handleOutgoingMessage(ctx, webhook)
		}
		if webhook.Private {
			logger.Info("Ignoring message: reason=private_note")
		}
	case "message_updated":
		return b.handleChatwootEdit(ctx, webhook)
	case "conversation_created", "conversation_updated", "conversation_status_changed":
		logger.Debug("conversation lifecycle event", zap.String("event", webhook.Event))
	default:
		logger.Info("unhandled chatwoot event", zap.String("event", webhook.Event))
	}
	return nil
}

func (b *BridgeService) handleOutgoingMessage(ctx context.Context, webhook models.ChatwootWebhook) (retErr error) {
	logger := zap.L().With(zap.Int("message_id", webhook.ID), zap.String("content_type", webhook.ContentType))

	if b.metrics != nil {
		b.metrics.OutboundTotal.Add(1)
		defer func() {
			if retErr != nil {
				b.metrics.OutboundError.Add(1)
			} else {
				b.metrics.OutboundSuccess.Add(1)
			}
		}()
	}

	logger.Info("Message metadata",
		zap.String("message_type", webhook.MessageType),
		zap.String("content_type", webhook.ContentType),
		zap.Int("attachment_count", len(webhook.Attachments)),
	)

	// 1. Sanidade: confirma que o webhook é da única conta Chatwoot configurada.
	// Um account_id divergente indica configuração incorreta (webhook apontando
	// para o bridge errado, ou CHATWOOT_ACCOUNT_ID desatualizado).
	if webhook.Account.ID != b.chatwoot.AccountID {
		logger.Error("chatwoot webhook account_id does not match configured CHATWOOT_ACCOUNT_ID",
			zap.Int("payload_account_id", webhook.Account.ID),
			zap.Int("configured_account_id", b.chatwoot.AccountID),
		)
		if b.metrics != nil {
			b.metrics.Error404Count.Add(1)
		}
		return fmt.Errorf("unexpected chatwoot account_id: %d", webhook.Account.ID)
	}

	// 2. Extrai phone_number direto do payload (conversation.meta.sender.phone_number)
	// Evolution GO espera número sem "+": "5522988010114", não "+5522988010114"
	phoneNumber := strings.TrimPrefix(webhook.Conversation.Meta.Sender.PhoneNumber, "+")
	if phoneNumber == "" {
		logger.Error("phone_number not found in conversation.meta.sender",
			zap.Int("conversation_id", webhook.Conversation.ID),
			zap.String("sender_name", webhook.Conversation.Meta.Sender.Name),
		)
		return fmt.Errorf("phone_number not found in conversation.meta.sender")
	}
	logger.Info("Phone number extracted from conversation.meta.sender", zap.String("phone_number", phoneNumber))

	// 3. Resolve a instância Evolution a partir de conversation.custom_attributes
	// (gravado no inbound — ver handleIncomingMessage), com fallback pra
	// instância default. O token usado no envio é sempre o da MESMA instância
	// resolvida aqui — antes desta reescrita, o token vinha de uma segunda
	// lookup separada por "instância default do tenant", o que quebraria (envio
	// com token errado) para qualquer conversa presa a uma instância não-default
	// agora que múltiplas instâncias reais coexistem.
	instanceName, _ := webhook.Conversation.CustomAttributes["evolution_instance"].(string)
	var instance *models.EvolutionInstance
	var err error
	if instanceName != "" {
		instance, err = b.instanceRepo.FindByInstanceName(ctx, instanceName)
		if err != nil {
			logger.Warn("evolution_instance from custom_attributes not found, falling back to default",
				zap.String("instance_name", instanceName), zap.Error(err))
			instance = nil
		} else {
			logger.Info("Evolution instance resolved from conversation.custom_attributes", zap.String("instance_name", instanceName))
		}
	}
	if instance == nil {
		instance, err = b.instanceRepo.FindDefault(ctx)
		if err != nil {
			return fmt.Errorf("no evolution instance available: %w", err)
		}
		instanceName = instance.InstanceName
		logger.Info("Evolution instance resolved via fallback", zap.String("instance_name", instanceName))
	}

	instanceToken := ""
	if instance.APIKey != nil {
		instanceToken = *instance.APIKey
	}

	logger.Info("Iniciando disparo para WhatsApp",
		zap.String("phone_number", phoneNumber),
		zap.String("instance_name", instanceName),
		zap.String("content_type", webhook.ContentType),
	)

	var resp *models.EvolutionSendResponse

	if len(webhook.Attachments) > 0 {
		att := webhook.Attachments[0]

		// att.FileType vem do Chatwoot normalizado: "image", "video", "audio", "file"
		// "file" (documento genérico no Chatwoot) → "document" (Evolution GO)
		// Nota: att.URL (data_url) aponta para o host EasyPanel do Chatwoot — acessível
		// pela Evolution Go via HTTPS público (mesmo projeto EasyPanel, via Traefik).
		mediaType := att.FileType
		if mediaType == "file" {
			mediaType = "document"
		}

		logger.Info("Dispatching media message",
			zap.String("media_type", mediaType),
			zap.String("filename", att.FileName),
			zap.String("url", att.URL),
		)
		resp, err = b.evolutionClient.SendMedia(ctx, instanceToken, instanceName, models.EvolutionSendMediaRequest{
			Number:   phoneNumber,
			URL:      att.URL,
			Type:     mediaType,
			Filename: att.FileName,
			Caption:  webhook.Content, // legenda do agente, pode ser vazia
		})
	} else {
		// Texto puro — links incluídos (WhatsApp gera link preview automaticamente)
		if webhook.Content == "" {
			logger.Warn("no content and no attachments, skipping")
			return nil
		}
		resp, err = b.evolutionClient.SendText(ctx, instanceToken, instanceName, models.EvolutionSendTextRequest{
			Number: phoneNumber,
			Text:   webhook.Content,
		})
	}
	if err != nil {
		logger.Error("failed to send message to evolution", zap.Error(err))
		return err
	}

	logger.Info("message sent to WhatsApp",
		zap.String("evolution_message_id", resp.MessageID),
		zap.String("to", phoneNumber),
	)
	return nil
}

func (b *BridgeService) handleChatwootEdit(ctx context.Context, webhook models.ChatwootWebhook) error {
	// Placeholder para edição – requer mapeamento de evolution_message_id
	zap.L().Debug("edit from chatwoot not yet implemented", zap.Int("message_id", webhook.ID))
	return nil
}
