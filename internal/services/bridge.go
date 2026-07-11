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

	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/observability"
	"github.com/linkkotech/bridge/internal/repository"
	"github.com/linkkotech/bridge/pkg/metaconfig"
)

type BridgeService struct {
	tenantRepo          repository.TenantRepository
	instanceRepo        repository.InstanceRepository
	inboxRepo           *repository.InboxRepository
	eventRepo           repository.AddonEventRepository
	evolutionClient     *EvolutionClient
	mediaService        *MediaService
	cacheService        *CacheService
	callbackService     *CallbackService
	metrics             *observability.MetricsCollector
	reconnectInProgress sync.Map
}

func NewBridgeService(
	tenantRepo repository.TenantRepository,
	instanceRepo repository.InstanceRepository,
	inboxRepo *repository.InboxRepository,
	eventRepo repository.AddonEventRepository,
	evolutionClient *EvolutionClient,
	mediaService *MediaService,
) *BridgeService {
	return &BridgeService{
		tenantRepo:      tenantRepo,
		instanceRepo:    instanceRepo,
		inboxRepo:       inboxRepo,
		eventRepo:       eventRepo,
		evolutionClient: evolutionClient,
		mediaService:    mediaService,
	}
}

func (b *BridgeService) SetCacheService(cache *CacheService) {
	b.cacheService = cache
}

func (b *BridgeService) SetCallbackService(cb *CallbackService) {
	b.callbackService = cb
}

func (b *BridgeService) SetMetrics(m *observability.MetricsCollector) {
	b.metrics = m
}

// ValidateChatwootWebhookSecret valida a assinatura HMAC-SHA256 do webhook Chatwoot.
func (b *BridgeService) ValidateChatwootWebhookSecret(ctx context.Context, tenantID string, r *nethttp.Request) bool {
	tenant, err := b.tenantRepo.FindByID(ctx, tenantID)
	if err != nil || tenant == nil {
		return false
	}
	// Se não há segredo configurado, aceita qualquer request
	if tenant.ChatwootWebhookSecret == nil || *tenant.ChatwootWebhookSecret == "" {
		zap.L().Warn("chatwoot_webhook_secret not set for tenant, accepting request", zap.String("tenant_id", tenantID))
		return true
	}
	secret := strings.TrimSpace(*tenant.ChatwootWebhookSecret)

	// Log de identidade do segredo (nunca loga o segredo completo)
	secretPreview := secret
	if len(secretPreview) >= 6 {
		secretPreview = secretPreview[:3] + "..." + secretPreview[len(secretPreview)-3:]
	}
	zap.L().Info("chatwoot HMAC: secret identity",
		zap.String("tenant_id", tenantID),
		zap.Int("secret_len", len(secret)),
		zap.String("secret_preview", secretPreview),
	)

	// Chatwoot assina o body com HMAC-SHA256 e envia X-Chatwoot-Signature: sha256=<hex>
	sigHeader := r.Header.Get("X-Chatwoot-Signature")
	if sigHeader == "" {
		zap.L().Warn("missing X-Chatwoot-Signature header", zap.String("tenant_id", tenantID))
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
			zap.String("tenant_id", tenantID),
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

	var tenant *models.Tenant
	var instance *models.EvolutionInstance
	var err error

	// Estratégia 1: Se temos TenantID e InstanceName da URL, busca diretamente
	if webhook.TenantID != "" {
		tenant, err = b.tenantRepo.FindByID(ctx, webhook.TenantID)
		if err != nil {
			logger.Error("tenant not found by id from URL param", zap.Error(err))
			return fmt.Errorf("tenant %s not found: %w", webhook.TenantID, err)
		}
		instance, err = b.instanceRepo.FindDefaultByTenantID(ctx, webhook.TenantID)
		if err != nil {
			logger.Error("instance not found for tenant", zap.Error(err))
			return fmt.Errorf("instance for tenant %s not found: %w", webhook.TenantID, err)
		}
	} else {
		// Estratégia 2: Fallback via InstanceName do webhook
		instance, err = b.instanceRepo.FindByInstanceName(ctx, webhook.InstanceName)
		if err != nil {
			logger.Error("instance not found by name", zap.Error(err))
			return fmt.Errorf("instance %s not found: %w", webhook.InstanceName, err)
		}
		tenant, err = b.tenantRepo.FindByID(ctx, instance.TenantID)
		if err != nil {
			logger.Error("tenant not found", zap.Error(err))
			return fmt.Errorf("tenant %s not found: %w", instance.TenantID, err)
		}
	}

	_ = instance

	if tenant.ChatwootAccountID == nil || tenant.ChatwootAPIToken == nil {
		logger.Info("skipping chatwoot forwarding for evolution-only tenant")
		return nil
	}

	chatwootClient := NewChatwootAdminClient(
		tenant.ChatwootInternalURL(),
		*tenant.ChatwootAPIToken,
	)

	accountID := *tenant.ChatwootAccountID

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

	logger.Info("processing incoming message",
		zap.Int("account_id", accountID),
		zap.String("phone_number", phoneNumber),
		zap.String("chatwoot_url", tenant.ChatwootInternalURL()),
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

	// 4. Encontra ou cria conversa
	inboxID, err := b.inboxRepo.GetDefaultInboxID(ctx, tenant.ID)
	if err != nil {
		// Fallback: busca o primeiro inbox no Chatwoot e salva localmente
		logger.Warn("inbox not in local DB, fetching from Chatwoot", zap.Error(err))
		inbox, inboxErr := chatwootClient.GetFirstInbox(ctx, accountID, *tenant.ChatwootAPIToken)
		if inboxErr != nil {
			logger.Error("failed to get inbox from chatwoot", zap.Error(inboxErr))
			return inboxErr
		}
		inboxID = inbox.ID
		// Salva para próximas requisições
		_ = b.inboxRepo.SaveInbox(ctx, tenant.ID, accountID, inbox.ID, inbox.Name)
		logger.Info("inbox fetched from chatwoot and cached", zap.Int("inbox_id", inboxID))
	}

	conversation, err := chatwootClient.FindOrCreateConversation(ctx, accountID, inboxID, contact.ID)
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
		// Re-fetch inbox (invalida inboxID em memória)
		if freshInbox, inboxErr := chatwootClient.GetFirstInbox(ctx, accountID, *tenant.ChatwootAPIToken); inboxErr == nil {
			inboxID = freshInbox.ID
			_ = b.inboxRepo.SaveInbox(ctx, tenant.ID, accountID, freshInbox.ID, freshInbox.Name)
		}
		// Re-fetch conversa com IDs frescos
		freshConv, recovErr := chatwootClient.FindOrCreateConversation(ctx, accountID, inboxID, freshContact.ID)
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

	// Notifica o Next.js (mesmo padrão que meta_webhook.go já usa pro provider
	// Meta) — permite correlacionar a resposta com um pedido pendente (RIE do
	// Event Bus, consentimento LGPD, etc). Best-effort, fire-and-forget: nunca
	// bloqueia nem falha o forward pro Chatwoot acima, que é o caminho
	// principal já em produção.
	//
	// LIMITAÇÃO CONHECIDA: só dispara pra tenants com Chatwoot configurado
	// (workspaces "evolution-only" retornam antes de chegar aqui — ver early
	// return logo no início desta função). Reavaliar se algum dia isso importar.
	if b.callbackService != nil && messageReq.Content != "" {
		// wa_message_logs.recipient_phone é gravado só com dígitos (sem "+") —
		// phoneNumber aqui está em E.164 (com "+"), preciso normalizar antes
		// de filtrar, senão o lookup nunca bate.
		lookupPhone := strings.TrimPrefix(phoneNumber, "+")
		eventID, _, err := metaconfig.LookupEventIDByPhone(webhook.InstanceName, lookupPhone)
		if err != nil {
			logger.Warn("lookup event_id by phone failed, callback segue sem correlação", zap.Error(err))
		}
		go b.callbackService.Notify(context.Background(), CallbackPayload{
			Event:        "message.received",
			LogID:        eventID,
			InstanceName: webhook.InstanceName,
			Provider:     "evolution",
			SentAt:       time.Now(),
			Reply: &CallbackReplyDetail{
				From:        phoneNumber,
				Text:        messageReq.Content,
				ReplyAt:     time.Now().UTC().Format(time.RFC3339),
				MessageType: "text",
			},
		})
	}

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

	workspaceID, instanceID, err := b.instanceRepo.GetWorkspaceAndID(ctx, webhook.InstanceName)
	if err != nil {
		logger.Warn("instance not found for connection.update", zap.Error(err))
		return nil
	}

	// Evento "Connected" da Evolution GO: { jid, pushName, status: "open" }
	// O jid vem no formato "5511999998888:98@s.whatsapp.net"
	if payload.Jid != "" && payload.Status == "open" {
		jidClean := strings.SplitN(payload.Jid, "@", 2)[0]         // remove @s.whatsapp.net
		jidClean = strings.SplitN(jidClean, ":", 2)[0]             // remove sufixo :XX do device
		phone := "+" + jidClean

		logger.Info("Connected event received", zap.String("phone", phone), zap.String("push_name", payload.PushName))

		if err = b.instanceRepo.UpdateConnectionState(ctx, webhook.InstanceName, "open", &phone); err != nil {
			logger.Error("Connected: failed to update connection state, callback will NOT be sent",
				zap.String("instance", webhook.InstanceName),
				zap.Error(err))
			return nil
		}

		if b.cacheService != nil {
			b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
			b.cacheService.ClearInstanceRateLimits(ctx, webhook.InstanceName)
		}

		b.eventRepo.InsertEvent(ctx, workspaceID, instanceID, "evolution", "connected", "webhook_bridge", map[string]interface{}{
			"reason":    "connected",
			"phone":     phone,
			"push_name": payload.PushName,
		})

		logger.Info("Connected: sending callback to platform",
			zap.String("instance", webhook.InstanceName),
			zap.String("workspace_id", workspaceID))

		if b.callbackService != nil {
			// [DIAGNÓSTICO] chamada síncrona para capturar erro no log
			if err := b.callbackService.Notify(ctx, CallbackPayload{
				EventType:    "connection.open",
				TenantID:     webhook.TenantID,
				WorkspaceID:  workspaceID,
				InstanceName: webhook.InstanceName,
				PhoneNumber:  phone,
				Status:       "connected",
				Reason:       "connected",
				SentAt:       time.Now(),
			}); err != nil {
				logger.Error("callback notify error", zap.Error(err))
			} else {
				logger.Info("callback notify ok")
			}
		}
		return nil
	}

	// Fallback: Evolution GO mais antigo envia state:"open" + wuid em vez de jid
	if payload.State == "open" && payload.Wuid != "" {
		phone := "+" + strings.Replace(payload.Wuid, "@s.whatsapp.net", "", 1)

		if err = b.instanceRepo.UpdateConnectionState(ctx, webhook.InstanceName, "open", &phone); err != nil {
			logger.Error("failed to update instance state to open (wuid fallback)", zap.Error(err))
			return nil
		}

		if b.cacheService != nil {
			b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
			b.cacheService.ClearInstanceRateLimits(ctx, webhook.InstanceName)
		}

		b.eventRepo.InsertEvent(ctx, workspaceID, instanceID, "evolution", "connected", "webhook_bridge", map[string]interface{}{
			"reason": "connected",
			"phone":  phone,
		})

		if b.callbackService != nil {
			go b.callbackService.Notify(context.Background(), CallbackPayload{
				EventType:    "connection.open",
				TenantID:     webhook.TenantID,
				WorkspaceID:  workspaceID,
				InstanceName: webhook.InstanceName,
				PhoneNumber:  phone,
				Status:       "connected",
				Reason:       "connected",
				SentAt:       time.Now(),
			})
		}
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
	workspaceID, _, _ := b.instanceRepo.GetWorkspaceAndID(ctx, webhook.InstanceName)

	err := b.instanceRepo.UpdateConnectionState(ctx, webhook.InstanceName, "close", nil)
	if err != nil {
		logger.Warn("failed to update instance state to disconnected for qr timeout", zap.Error(err))
	}

	if b.cacheService != nil {
		b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
	}

	if b.callbackService != nil {
		go b.callbackService.Notify(context.Background(), CallbackPayload{
			EventType:    "connection.close",
			TenantID:     webhook.TenantID,
			WorkspaceID:  workspaceID,
			InstanceName: webhook.InstanceName,
			Status:       "disconnected",
			Reason:       "qrcode_timeout",
			SentAt:       time.Now(),
		})
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
				tenantID := webhook.TenantID
				go func() {
					defer b.reconnectInProgress.Delete(instanceName)
					time.Sleep(2 * time.Second)
					webhookURL := fmt.Sprintf("%s/webhook/evolution?instance=%s&tenant=%s",
						os.ExpandEnv("${WEBHOOK_BASE_URL}"), instanceName, tenantID)
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
	var payload struct {
		Reason string `json:"reason"`
	}
	json.Unmarshal(webhook.Data, &payload)

	workspaceID, instanceID, err := b.instanceRepo.GetWorkspaceAndID(ctx, webhook.InstanceName)
	if err != nil {
		logger.Warn("instance not found for logout", zap.Error(err))
		return nil
	}

	err = b.instanceRepo.LogoutInstance(ctx, webhook.InstanceName)
	if err != nil {
		logger.Error("failed to logout instance in db", zap.Error(err))
		return nil
	}

	if b.cacheService != nil {
		b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
	}

	b.eventRepo.InsertEvent(ctx, workspaceID, instanceID, "evolution", "disconnected", "webhook_bridge", map[string]interface{}{
		"reason": "logout",
	})

	if b.callbackService != nil {
		go b.callbackService.Notify(context.Background(), CallbackPayload{
			EventType:    "connection.close",
			TenantID:     webhook.TenantID,
			WorkspaceID:  workspaceID,
			InstanceName: webhook.InstanceName,
			Status:       "disconnected",
			Reason:       "logout",
			SentAt:       time.Now(),
		})
	}

	return nil
}

func (b *BridgeService) handleInstanceDeleted(ctx context.Context, webhook models.EvolutionWebhook, logger *zap.Logger) error {
	workspaceID, instanceID, err := b.instanceRepo.GetWorkspaceAndID(ctx, webhook.InstanceName)
	if err != nil {
		logger.Warn("instance not found for deleted event", zap.Error(err))
		return nil
	}

	err = b.instanceRepo.DeleteInstance(ctx, webhook.InstanceName)
	if err != nil {
		logger.Error("failed to mark instance as deleted", zap.Error(err))
		return nil
	}

	if b.cacheService != nil {
		b.cacheService.DeleteQRCode(ctx, webhook.InstanceName)
		b.cacheService.ClearInstanceRateLimits(ctx, webhook.InstanceName)
	}

	b.eventRepo.InsertEvent(ctx, workspaceID, instanceID, "evolution", "deleted", "webhook_bridge", map[string]interface{}{
		"reason": "instance_deleted_on_evolution",
	})

	if b.callbackService != nil {
		go b.callbackService.Notify(context.Background(), CallbackPayload{
			EventType:    "instance.deleted",
			TenantID:     webhook.TenantID,
			WorkspaceID:  workspaceID,
			InstanceName: webhook.InstanceName,
			Status:       "deleted",
			Reason:       "instance_deleted",
			SentAt:       time.Now(),
		})
	}

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

	// 1. Encontra tenant pelo account_id
	tenant, err := b.tenantRepo.FindByChatwootAccountID(ctx, webhook.Account.ID)
	if err != nil {
		logger.Error("tenant not found for Chatwoot Account",
			zap.Int("account_id", webhook.Account.ID),
			zap.Error(err),
		)
		if b.metrics != nil {
			b.metrics.Error404Count.Add(1)
		}
		return err
	}
	logger.Info("Tenant resolved", zap.String("tenant_id", tenant.ID))

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

	// 3. Extrai evolution_instance de conversation.custom_attributes
	instanceName, _ := webhook.Conversation.CustomAttributes["evolution_instance"].(string)
	if instanceName == "" {
		// fallback para instância padrão do tenant
		instance, err := b.instanceRepo.FindDefaultByTenantID(ctx, tenant.ID)
		if err != nil {
			return fmt.Errorf("no evolution instance for tenant: %w", err)
		}
		instanceName = instance.InstanceName
		logger.Info("Evolution instance resolved via fallback", zap.String("instance_name", instanceName))
	} else {
		logger.Info("Evolution instance resolved from conversation.custom_attributes", zap.String("instance_name", instanceName))
	}

	// 3. Obtém token da instância para autenticação no envio
	instance, err := b.instanceRepo.FindDefaultByTenantID(ctx, tenant.ID)
	if err != nil {
		logger.Error("failed to find evolution instance for tenant", zap.Error(err))
		return err
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
