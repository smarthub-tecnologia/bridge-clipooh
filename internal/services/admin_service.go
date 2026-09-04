package services

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/linkkotech/bridge/internal/config"
	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/repository"
	"go.uber.org/zap"
)

// AdminService cuida do provisionamento/manutenção de instâncias Evolution GO
// e de operações administrativas na única conta Chatwoot da Cartão Pro
// (AccountID/APIToken fixos, ver config.ChatwootConfig). Não há mais conceito
// de tenant: a conta Chatwoot já existe e não é criada nem removida por aqui.
type AdminService struct {
	db               *pgxpool.Pool
	instanceRepo     repository.InstanceRepository
	inboxRepo        *repository.InboxRepository
	chatwootUserRepo *repository.ChatwootUserRepository
	chatwootAdmin    *ChatwootAdminClient
	evolution        *EvolutionClient
	webhookBaseURL   string
	chatwoot         config.ChatwootConfig
}

func NewAdminService(
	instanceRepo repository.InstanceRepository,
	inboxRepo *repository.InboxRepository,
	chatwootUserRepo *repository.ChatwootUserRepository,
	db *pgxpool.Pool,
	chatwootAdmin *ChatwootAdminClient,
	evolution *EvolutionClient,
	webhookBaseURL string,
	chatwootCfg config.ChatwootConfig,
) *AdminService {
	return &AdminService{
		db:               db,
		instanceRepo:     instanceRepo,
		inboxRepo:        inboxRepo,
		chatwootUserRepo: chatwootUserRepo,
		chatwootAdmin:    chatwootAdmin,
		evolution:        evolution,
		webhookBaseURL:   webhookBaseURL,
		chatwoot:         chatwootCfg,
	}
}

// CreateInstanceRequest payload recebido do dashboard para provisionar uma
// nova linha WhatsApp (instância Evolution GO) na conta Cartão Pro já existente.
type CreateInstanceRequest struct {
	InstanceName string `json:"instance_name"`
}

// CreateInstanceResponse resposta da criação de instância.
type CreateInstanceResponse struct {
	InstanceName string `json:"instance_name"`
	Status       string `json:"status"`
}

// CreateInstance provisiona uma nova instância Evolution GO (linha WhatsApp)
// e configura o webhook dela. Não cria nada no Chatwoot — a conta, o usuário
// admin e a inbox "api" já existem fixos (CHATWOOT_ACCOUNT_ID/API_TOKEN) desde
// que o bridge deixou de ser multi-tenant.
func (s *AdminService) CreateInstance(ctx context.Context, req CreateInstanceRequest) (*CreateInstanceResponse, error) {
	logger := zap.L().With(zap.String("operation", "create_instance"))

	// TEMP DEBUG — remover após diagnóstico de webhook_url duplicada / token sobrescrito
	logger.Info("DEBUG raw webhookBaseURL", zap.String("raw_webhook_base_url", s.webhookBaseURL))

	var instanceName string
	shortHash := uuid.New().String()[:6]
	if req.InstanceName != "" {
		baseName := strings.ToLower(strings.ReplaceAll(req.InstanceName, " ", "-"))
		instanceName = fmt.Sprintf("%s-%s", baseName, shortHash)
	} else {
		instanceName = fmt.Sprintf("inst-%s", shortHash)
	}

	evolutionInstanceID := uuid.New().String()
	instanceToken := uuid.New().String()
	evolutionBaseURL := os.Getenv("EVOLUTION_BASE_URL")

	instanceReq := models.EvolutionCreateInstanceRequest{
		Name:       instanceName,
		InstanceID: evolutionInstanceID,
		Token:      instanceToken,
		AdvancedSettings: &models.EvolutionAdvancedSettings{
			AlwaysOnline:  true,
			IgnoreGroups:  true,
			IgnoreStatus:  true,
			MsgRejectCall: "This number does not accept calls. Please send a text message.",
			ReadMessages:  true,
			RejectCall:    true,
		},
	}
	evolutionResp, err := s.evolution.CreateInstance(ctx, instanceReq)
	if err != nil {
		logger.Error("failed to create evolution go instance", zap.Error(err))
		return nil, fmt.Errorf("failed to create evolution go instance: %w", err)
	}
	if evolutionResp.InstanceID == "" {
		evolutionResp.InstanceID = evolutionInstanceID
	}
	if evolutionResp.Name == "" {
		evolutionResp.Name = instanceName
	}

	// TEMP DEBUG — remover após diagnóstico de webhook_url duplicada / token sobrescrito
	logger.Info("DEBUG evolution create instance response", zap.Any("evolution_create_response", evolutionResp))

	evolutionResp.Token = instanceToken

	expandedWebhookBase := os.ExpandEnv(s.webhookBaseURL)
	evoWebhookSecret := os.Getenv("WEBHOOK_SECRET_EVOLUTION")
	evolutionWebhookUrl := fmt.Sprintf("%s/webhook/evolution?instance=%s", expandedWebhookBase, evolutionResp.Name)
	if evoWebhookSecret != "" {
		evolutionWebhookUrl += "&token=" + evoWebhookSecret
	}

	newInstance := &models.EvolutionInstance{
		InstanceID:   evolutionResp.InstanceID,
		InstanceName: evolutionResp.Name,
		APIKey:       &evolutionResp.Token,
		BaseURL:      &evolutionBaseURL,
		WebhookURL:   &evolutionWebhookUrl,
		Status:       "created",
	}

	// Conecta e configura Webhook na Evolution GO — falha aqui não impede o
	// provisionamento (o webhook pode ser reconfigurado depois via /connect),
	// só fica registrado no log.
	connectReq := models.EvolutionConnectRequest{
		Immediate:  false,
		WebhookUrl: evolutionWebhookUrl,
		Subscribe: []string{
			"MESSAGE",
			"CONNECTION",
			"QRCODE",
		},
	}
	if err := s.evolution.ConnectInstance(ctx, evolutionResp.Token, connectReq); err != nil {
		logger.Error("failed to connect evolution go instance and set webhook — webhook not configured",
			zap.String("instance_id", evolutionResp.InstanceID),
			zap.Error(err),
		)
	}

	if err := s.instanceRepo.Create(ctx, newInstance); err != nil {
		logger.Error("CRITICAL: evolution instance created in API but failed to persist to DB — orphan instance",
			zap.String("instance_id", newInstance.InstanceID),
			zap.String("instance_name", newInstance.InstanceName),
			zap.Error(err),
		)
		return nil, fmt.Errorf("failed to save instance: %w", err)
	}

	logger.Info("instance provisioned successfully", zap.String("instance_name", evolutionResp.Name))

	return &CreateInstanceResponse{
		InstanceName: evolutionResp.Name,
		Status:       "created",
	}, nil
}

// CreateWidgetInboxResponse é o retorno do endpoint admin de criação de inbox
// Website — devolvido puro pro Next.js persistir (o Go não grava nada aqui,
// só fala com o Chatwoot, igual o resto deste service).
type CreateWidgetInboxResponse struct {
	InboxID               int    `json:"inbox_id"`
	WebsiteToken          string `json:"website_token"`
	IdentityValidationKey string `json:"identity_validation_key,omitempty"`
	// WebhookSecret é o segredo HMAC do webhook de CONTA (não desta inbox
	// especificamente — Channel::WebWidget não suporta webhook por-inbox, ver
	// comentário em models/chatwoot.go). Vazio se NEXTJS_APP_URL não estiver
	// configurado (funcionalidade em tempo real degrada, o resto do addon
	// continua funcionando).
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// widgetWebhookSubscriptions — o que a Fase B/C precisam: resposta do agente
// chegando ao vivo (message_created) + indicador de digitação (typing).
// message_updated fica de fora por ora (não usado ainda).
var widgetWebhookSubscriptions = []string{"message_created", "conversation_typing_on", "conversation_typing_off"}

// ensureWidgetWebhook garante que existe (idempotente) um webhook DE CONTA
// apontando pro endpoint público do Next.js, e devolve o secret HMAC dele.
// Nunca cria duplicado — account.webhooks tem UNIQUE(account_id, url); se já
// existe um com essa URL, reaproveita o secret já gerado em vez de tentar
// criar de novo (que devolveria 422). Se o webhook já existir mas faltar
// algum evento de widgetWebhookSubscriptions (ex.: contas provisionadas antes
// do typing ser adicionado ao conjunto padrão), faz PATCH pra migrar —
// necessário porque reaproveitar por URL, sozinho, nunca atualizaria contas
// antigas.
func (s *AdminService) ensureWidgetWebhook(ctx context.Context, accountID int, accountToken string) (string, error) {
	nextjsAppURL := strings.TrimSuffix(os.Getenv("NEXTJS_APP_URL"), "/")
	if nextjsAppURL == "" {
		zap.L().Warn("NEXTJS_APP_URL não configurado — pulando registro do webhook do widget (tempo real ficará indisponível)")
		return "", nil
	}
	webhookURL := nextjsAppURL + "/api/public/chat-widget/webhook"

	existing, err := s.chatwootAdmin.ListAccountWebhooks(ctx, accountID, accountToken)
	if err != nil {
		return "", fmt.Errorf("failed to list account webhooks: %w", err)
	}
	for _, wh := range existing {
		if wh.URL == webhookURL {
			if missing := missingSubscriptions(wh.Subscriptions, widgetWebhookSubscriptions); len(missing) > 0 {
				zap.L().Info("migrating widget account webhook subscriptions",
					zap.Int("account_id", accountID), zap.Int("webhook_id", wh.ID), zap.Strings("adding", missing))
				merged := append(append([]string{}, wh.Subscriptions...), missing...)
				if err := s.chatwootAdmin.UpdateAccountWebhook(ctx, accountID, wh.ID, accountToken, webhookURL, merged); err != nil {
					// Non-fatal: o webhook antigo continua funcionando pro que já assinava
					// (message_created); só o typing fica indisponível até resolver.
					zap.L().Warn("failed to migrate widget webhook subscriptions (non-fatal)",
						zap.Int("account_id", accountID), zap.Error(err))
				}
			}
			return wh.Secret, nil
		}
	}

	created, err := s.chatwootAdmin.CreateAccountWebhook(ctx, accountID, accountToken, webhookURL, widgetWebhookSubscriptions)
	if err != nil {
		return "", fmt.Errorf("failed to create account webhook: %w", err)
	}
	return created.Secret, nil
}

// profileCustomAttributeDefinitions são os 2 Custom Attribute Definitions que
// precisam existir na conta pra conversation.custom_attributes gravar
// profile_id/profile_slug de fato — consumidos por sendPublicVisitorMessage
// (Next.js, visibilidade do agente humano no transbordo) e, futuramente,
// pelas tools do agente de IA SDR (precisa do profile_id pra taguear o
// contato dono do cartão que originou a conversa).
var profileCustomAttributeDefinitions = []models.ChatwootCustomAttributeDefinition{
	{
		AttributeDisplayName: "Profile ID",
		AttributeKey:         "profile_id",
		AttributeDisplayType: "text",
		AttributeModel:       "conversation_attribute",
	},
	{
		AttributeDisplayName: "Profile Slug",
		AttributeKey:         "profile_slug",
		AttributeDisplayType: "text",
		AttributeModel:       "conversation_attribute",
	},
}

// ensureProfileCustomAttributes garante (idempotente, best-effort) que os
// Custom Attribute Definitions acima existem na conta — lista antes de criar
// (mesmo padrão de ensureWidgetWebhook) pra nunca disparar o 422 de
// attribute_key duplicado. Nunca retorna erro: falhar aqui não pode abortar
// o provisionamento do combo nem o sync de tenants antigos, só loga.
func (s *AdminService) ensureProfileCustomAttributes(ctx context.Context, accountID int, accountToken string) {
	logger := zap.L().With(zap.String("operation", "ensure_profile_custom_attributes"), zap.Int("account_id", accountID))

	existing, err := s.chatwootAdmin.ListCustomAttributeDefinitions(ctx, accountID, accountToken)
	if err != nil {
		logger.Warn("failed to list custom attribute definitions (non-fatal)", zap.Error(err))
		existing = nil
	}

	have := make(map[string]bool, len(existing))
	for _, def := range existing {
		have[def.AttributeKey] = true
	}

	for _, def := range profileCustomAttributeDefinitions {
		if have[def.AttributeKey] {
			continue
		}
		if _, err := s.chatwootAdmin.CreateCustomAttributeDefinition(ctx, accountID, accountToken, def); err != nil {
			logger.Warn("failed to create custom attribute definition (non-fatal)",
				zap.String("attribute_key", def.AttributeKey), zap.Error(err))
			continue
		}
		logger.Info("custom attribute definition created", zap.String("attribute_key", def.AttributeKey))
	}
}

// missingSubscriptions devolve os itens de `desired` que não estão em `have`.
func missingSubscriptions(have []string, desired []string) []string {
	set := make(map[string]bool, len(have))
	for _, s := range have {
		set[s] = true
	}
	var missing []string
	for _, d := range desired {
		if !set[d] {
			missing = append(missing, d)
		}
	}
	return missing
}

// CreateWidgetInbox cria uma inbox tipo Website (widget público) na MESMA conta
// Chatwoot da Cartão Pro — usada pelo addon "Widget de Chat do Perfil Digital"
// (Next.js). Usa CHATWOOT_API_TOKEN fixo (o antigo fallback via
// GetAccountAccessToken só fazia sentido quando esse token vinha de um usuário
// criado por-tenant; agora é sempre o mesmo).
func (s *AdminService) CreateWidgetInbox(ctx context.Context, name string) (*CreateWidgetInboxResponse, error) {
	logger := zap.L().With(zap.String("operation", "create_widget_inbox"))

	accountID := s.chatwoot.AccountID
	chatwootAccessToken := s.chatwoot.APIToken

	inboxName := name
	if inboxName == "" {
		inboxName = "Widget do Perfil Digital"
	}

	// website_url é obrigatório na Chatwoot (422 sem ele) — só metadado exibido
	// no painel, não afeta o funcionamento do widget.
	websiteURL := "https://cartaopro.com"

	inbox, err := s.chatwootAdmin.CreateWebsiteInbox(ctx, accountID, chatwootAccessToken, inboxName, websiteURL)
	if err != nil {
		logger.Error("failed to create chatwoot website inbox", zap.Error(err))
		return nil, fmt.Errorf("failed to create chatwoot website inbox: %w", err)
	}

	logger.Info("chatwoot website inbox created",
		zap.Int("inbox_id", inbox.ID),
		zap.Bool("has_hmac_token", inbox.HMACToken != ""),
	)

	// Webhook de conta pro tempo real (Fase B) — best-effort: falha aqui NÃO
	// derruba a criação da inbox (o addon funciona sem tempo real; só a
	// Fase B fica indisponível até isso ser corrigido/reconfigurado).
	webhookSecret, err := s.ensureWidgetWebhook(ctx, accountID, chatwootAccessToken)
	if err != nil {
		logger.Warn("failed to ensure widget webhook (non-fatal, inbox already created)", zap.Error(err))
	}

	return &CreateWidgetInboxResponse{
		InboxID:               inbox.ID,
		WebsiteToken:          inbox.WebsiteToken,
		IdentityValidationKey: inbox.HMACToken,
		WebhookSecret:         webhookSecret,
	}, nil
}

// SyncWidgetWebhookResponse — igual CreateWidgetInboxResponse, mas só o
// campo que faz sentido pra uma sincronização (não recria inbox/identity key).
type SyncWidgetWebhookResponse struct {
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// SyncWidgetWebhook re-executa só a etapa de webhook de CreateWidgetInbox —
// nunca chama CreateWebsiteInbox, então é seguro rodar de novo (não duplica
// a inbox no Chatwoot). Existe pra migrar a conta se widgetWebhookSubscriptions
// ganhar novos eventos no futuro.
func (s *AdminService) SyncWidgetWebhook(ctx context.Context) (*SyncWidgetWebhookResponse, error) {
	accountID := s.chatwoot.AccountID
	chatwootAccessToken := s.chatwoot.APIToken

	s.ensureProfileCustomAttributes(ctx, accountID, chatwootAccessToken)

	webhookSecret, err := s.ensureWidgetWebhook(ctx, accountID, chatwootAccessToken)
	if err != nil {
		return nil, err
	}

	return &SyncWidgetWebhookResponse{
		WebhookSecret: webhookSecret,
	}, nil
}

// ── Agentes (aba "Usuários" do addon combo) ───────────────────────────────

// CreateAgentRequest payload recebido do Next.js pra vincular um profile do
// workspace como agente na conta Chatwoot da Cartão Pro.
type CreateAgentRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"` // "agent" ou "administrator" — default "agent"
}

// CreateAgentResponse — nunca inclui a senha (gerada e descartada na hora,
// ver generateAgentPassword). EmailSent indica se o disparo do e-mail nativo
// de definição de senha do Chatwoot (POST /auth/password) teve sucesso — não
// bloqueia a operação se falhar (ex. SMTP fora do ar), só sinaliza.
type CreateAgentResponse struct {
	AgentID   int    `json:"agent_id"`
	Email     string `json:"email"`
	EmailSent bool   `json:"email_sent"`
}

// generateAgentPassword gera uma senha aleatória forte só pra satisfazer a
// validação do Devise no CreateUser (config/initializers/secure_password.rb
// do Chatwoot exige 1 maiúscula, 1 minúscula, 1 número, 1 especial). Nunca é
// logada, devolvida ou persistida — só usada dentro do escopo desta chamada.
// Se o e-mail já existir como User em outra conta da instância, a Platform
// API ignora esta senha (User.from_email reaproveita o existente).
func generateAgentPassword() (string, error) {
	const upper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	const lower = "abcdefghjkmnpqrstuvwxyz"
	const digits = "23456789"
	const special = "@#$!%&*"
	all := upper + lower + digits + special

	randChar := func(pool string) (byte, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(pool))))
		if err != nil {
			return 0, err
		}
		return pool[n.Int64()], nil
	}

	chars := make([]byte, 0, 12)
	for _, pool := range []string{upper, lower, digits, special} {
		c, err := randChar(pool)
		if err != nil {
			return "", err
		}
		chars = append(chars, c)
	}
	for i := 0; i < 8; i++ {
		c, err := randChar(all)
		if err != nil {
			return "", err
		}
		chars = append(chars, c)
	}

	// Fisher-Yates shuffle
	for i := len(chars) - 1; i > 0; i-- {
		jBig, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		chars[i], chars[jBig.Int64()] = chars[jBig.Int64()], chars[i]
	}

	return string(chars), nil
}

// linkOrRollbackAgent vincula um User já criado a uma conta (AddUserToAccount)
// e, se falhar, só tenta compensar apagando o User globalmente (DeleteUser)
// quando temos certeza absoluta de que ele acabou de ser criado agora mesmo
// (user.Accounts veio vazio na resposta do CreateUser) — nunca quando a
// Platform API reaproveitou um usuário pré-existente (User.from_email), pra
// não apagar o acesso de alguém a contas que não têm nada a ver com esta
// operação.
func (s *AdminService) linkOrRollbackAgent(ctx context.Context, accountID int, user *models.ChatwootCreateUserResponse, role string) error {
	logger := zap.L().With(zap.String("operation", "link_or_rollback_agent"), zap.Int("account_id", accountID), zap.Int("user_id", user.ID))

	linkErr := s.chatwootAdmin.AddUserToAccount(ctx, accountID, models.ChatwootAddUserToAccountRequest{
		UserID: user.ID,
		Role:   role,
	})
	if linkErr == nil {
		return nil
	}

	wasNewUser := len(user.Accounts) == 0
	if wasNewUser {
		logger.Warn("failed to link newly created user to account, rolling back", zap.Error(linkErr))
		if delErr := s.chatwootAdmin.DeleteUser(ctx, user.ID); delErr != nil {
			logger.Error("rollback DeleteUser also failed — orphaned chatwoot user may remain", zap.Error(delErr))
		}
	} else {
		logger.Warn("failed to link PRE-EXISTING user to account — not rolling back (would delete their access elsewhere)", zap.Error(linkErr))
	}

	return linkErr
}

// CreateAgent vincula um profile do workspace como agente na conta Chatwoot
// da Cartão Pro (aba "Usuários" do addon combo). Dispara o e-mail nativo de
// definição de senha do Chatwoot — nunca gera nem devolve senha utilizável
// (ver generateAgentPassword).
func (s *AdminService) CreateAgent(ctx context.Context, req CreateAgentRequest) (*CreateAgentResponse, error) {
	logger := zap.L().With(zap.String("operation", "create_agent"))

	accountID := s.chatwoot.AccountID

	role := req.Role
	if role == "" {
		role = "agent"
	}

	password, err := generateAgentPassword()
	if err != nil {
		return nil, fmt.Errorf("failed to generate password: %w", err)
	}

	user, err := s.chatwootAdmin.CreateUser(ctx, models.ChatwootCreateUserRequest{
		Name:     req.Name,
		Email:    req.Email,
		Password: password,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create chatwoot user: %w", err)
	}

	if err := s.linkOrRollbackAgent(ctx, accountID, user, role); err != nil {
		return nil, fmt.Errorf("failed to link user to account: %w", err)
	}

	emailSent := true
	if err := s.chatwootAdmin.RequestPasswordReset(ctx, req.Email); err != nil {
		logger.Warn("failed to trigger chatwoot password-reset email (non-fatal, agent already created)", zap.Error(err))
		emailSent = false
	}

	return &CreateAgentResponse{
		AgentID:   user.ID,
		Email:     user.Email,
		EmailSent: emailSent,
	}, nil
}

// RemoveAgent desvincula um agente da conta Chatwoot da Cartão Pro — via
// Application API (mesma rota que o painel do Chatwoot usa), segura por
// desenho (só apaga o User globalmente se ele não tiver mais nenhum outro
// vínculo). 404 é tratado como sucesso pelo cliente (agente já não existia
// mais na conta).
func (s *AdminService) RemoveAgent(ctx context.Context, agentID int) error {
	accountID := s.chatwoot.AccountID
	accountToken := s.chatwoot.APIToken

	if err := s.chatwootAdmin.RemoveAgentFromAccount(ctx, accountID, agentID, accountToken); err != nil {
		return fmt.Errorf("failed to remove agent from account: %w", err)
	}
	return nil
}

// RunWebhookHealthChecker verifica periodicamente se o campo webhook está configurado
// em todas as instâncias da Evolution GO. Se encontrar webhook vazio, chama
// reconfigureWebhook para restaurá-lo automaticamente.
func (s *AdminService) RunWebhookHealthChecker(ctx context.Context, interval time.Duration) {
	logger := zap.L().With(zap.String("component", "webhook_health_checker"))
	logger.Info("webhook health checker started", zap.Duration("interval", interval))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("webhook health checker stopped")
			return
		case <-ticker.C:
			s.checkAllWebhooks(ctx)
		}
	}
}

func (s *AdminService) checkAllWebhooks(ctx context.Context) {
	logger := zap.L().With(zap.String("component", "webhook_health_checker"))

	instances, err := s.instanceRepo.FindAll(ctx)
	if err != nil {
		logger.Error("health check: failed to list instances", zap.Error(err))
		return
	}

	for _, inst := range instances {
		full, err := s.instanceRepo.FindByInstanceName(ctx, inst.InstanceName)
		if err != nil {
			logger.Warn("health check: failed to fetch instance record",
				zap.String("instance", inst.InstanceName),
				zap.Error(err),
			)
			continue
		}
		if full.InstanceID == "" || full.APIKey == nil {
			continue
		}

		info, err := s.evolution.GetInstanceInfo(ctx, full.InstanceID)
		if err != nil {
			logger.Warn("health check: failed to query evolution",
				zap.String("instance", full.InstanceName),
				zap.String("instance_id", full.InstanceID),
				zap.Error(err),
			)
			continue
		}

		if info.Webhook != "" {
			// Webhook is present, but let's check status reconciliation
			if !info.Connected && full.Status == "connected" {
				// (A) Se a instância retornar Connected: false na Evolution GO E o banco (evolution_instances) tiver status = "connected"
				full.Status = "disconnected"
				if err := s.instanceRepo.Update(ctx, full); err != nil {
					logger.Error("health check: failed to update instance status to disconnected", zap.Error(err))
				} else {
					logger.Info("health check: instance disconnected, updating status", zap.String("instance", full.InstanceName))
				}
			} else if info.Connected && full.Status != "connected" {
				// (B) Se a instância retornar Connected: true na Evolution GO E o banco tiver status != "connected"
				full.Status = "connected"
				if err := s.instanceRepo.Update(ctx, full); err != nil {
					logger.Error("health check: failed to update instance status to connected", zap.Error(err))
				} else {
					logger.Info("health check: instance reconnected, updating status", zap.String("instance", full.InstanceName))
				}
			}
			continue
		}

		logger.Warn("health check: webhook missing — triggering reconnect",
			zap.String("instance", full.InstanceName),
			zap.String("instance_id", full.InstanceID),
		)
		if err := s.reconfigureWebhook(ctx, full.InstanceName, ""); err != nil {
			logger.Error("health check: reconnect failed",
				zap.String("instance", full.InstanceName),
				zap.Error(err),
			)
		} else {
			logger.Info("health check: webhook restored",
				zap.String("instance", full.InstanceName),
			)
		}
	}
}

// reconfigureWebhook (re)configura o webhook na Evolution GO para a instância
// dada, sem gerar QR novo — usado internamente pelo health checker e como
// primeiro passo de ReconnectByInstanceName (fluxo público /connect). Se
// tokenOverride não for vazio, atualiza o APIKey no banco antes de reconectar.
func (s *AdminService) reconfigureWebhook(ctx context.Context, instanceName string, tokenOverride string) error {
	logger := zap.L().With(zap.String("instance", instanceName))

	instance, err := s.instanceRepo.FindByInstanceName(ctx, instanceName)
	if err != nil {
		return fmt.Errorf("instance not found: %w", err)
	}

	if tokenOverride != "" {
		instance.APIKey = &tokenOverride
		if err := s.instanceRepo.Update(ctx, instance); err != nil {
			logger.Warn("failed to update instance token in DB", zap.Error(err))
		} else {
			logger.Info("instance token updated in DB", zap.String("instance", instance.InstanceName))
		}
	}

	expandedWebhookBase := os.ExpandEnv(s.webhookBaseURL)
	webhookSecret := os.Getenv("WEBHOOK_SECRET_EVOLUTION")
	webhookUrl := fmt.Sprintf("%s/webhook/evolution?instance=%s", expandedWebhookBase, instance.InstanceName)
	if webhookSecret != "" {
		webhookUrl += "&token=" + webhookSecret
	}

	connectReq := models.EvolutionConnectRequest{
		Immediate:  false,
		WebhookUrl: webhookUrl,
		Subscribe: []string{
			"MESSAGE",
			"CONNECTION",
			"QRCODE",
		},
	}

	logger.Info("reconnecting evolution instance",
		zap.String("instance", instance.InstanceName),
		zap.String("instance_id", instance.InstanceID),
		zap.String("webhook_url", webhookUrl),
	)

	if err := s.evolution.ConnectInstance(ctx, *instance.APIKey, connectReq); err != nil {
		return fmt.Errorf("failed to reconnect instance: %w", err)
	}

	return nil
}

// ReconnectByInstanceName é o único fluxo público de reconexão de instância
// (POST /api/v1/instances/{instanceName}/connect). Reconfigura o webhook
// (reconfigureWebhook — mesma lógica usada pelo health checker automático),
// opcionalmente troca o token da instância primeiro, e então força a geração
// de um novo QR Code, tentando devolvê-lo inline (até 15s de polling).
func (s *AdminService) ReconnectByInstanceName(ctx context.Context, instanceName string, tokenOverride string) (qrBase64 string, err error) {
	logger := zap.L().With(zap.String("instance_name", instanceName))

	if err := s.reconfigureWebhook(ctx, instanceName, tokenOverride); err != nil {
		return "", err
	}

	instance, err := s.instanceRepo.FindByInstanceName(ctx, instanceName)
	if err != nil {
		return "", fmt.Errorf("instance not found: %w", err)
	}
	if instance.APIKey == nil {
		return "", fmt.Errorf("instance %s has no api key configured", instanceName)
	}

	// Verifica se a instância já está conectada na Evolution GO antes de iniciar um novo fluxo de QR.
	if info, err := s.evolution.GetInstanceInfo(ctx, instance.InstanceID); err == nil && info != nil && info.Connected {
		logger.Info("instance already connected in Evolution GO, skipping QR generation", zap.String("instance", instanceName))
		if instance.Status != "connected" {
			instance.Status = "connected"
			if updateErr := s.instanceRepo.Update(ctx, instance); updateErr != nil {
				logger.Error("failed to update instance status to connected", zap.Error(updateErr))
			}
		}
		return "ALREADY_CONNECTED", nil
	}

	logger.Info("triggering QR code generation via Evolution reconnect", zap.String("instance", instanceName))

	// Passo 1.5: POST /instance/reconnect para garantir geração de QR em cold start.
	// reconfigureWebhook (Immediate=false) configura o webhook mas pode não disparar
	// o QR imediatamente após logout completo. Reconnect força o processo explicitamente.
	// Não faz logout interno (diferente de ForceLogoutInstance) — seguro chamar aqui.
	if err := s.evolution.ReconnectEvolutionInstance(ctx, *instance.APIKey); err != nil {
		logger.Warn("reconnect instance returned error (non-fatal, prosseguindo com poll)", zap.Error(err))
	}

	// Passo 2: Respiro para o WebSocket abrir e o QR ser gerado
	time.Sleep(2 * time.Second)

	// Passo 3: Tenta retornar o QR inline (até 15s). Se não vier, retorna vazio
	// e o cliente busca via polling em /qr (caminho webhook → Redis → GET /qr).
	const maxAttempts = 15
	const pollInterval = time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		base64, pollErr := s.evolution.GetInstanceQR(ctx, *instance.APIKey)
		if pollErr != nil {
			logger.Warn("QR poll error", zap.Int("attempt", attempt), zap.Error(pollErr))
			continue
		}
		if base64 != "" {
			logger.Info("QR code ready", zap.Int("attempt", attempt))
			return base64, nil
		}
		logger.Debug("QR not ready yet", zap.Int("attempt", attempt))
	}

	// Return empty — caller should tell client to poll via webhook path (fallback)
	return "", nil
}

// RecreateInstance deleta a instância corrompida na Evolution GO e a recria com o mesmo
// nome, instanceID e token. Útil quando a instância está em estado "client disconnected"
// que impede a geração de QR Code. ConnectInstance é chamado após recreate para configurar
// o webhook — a geração do QR é tratada pelo fluxo TriggerConnect separadamente.
func (s *AdminService) RecreateInstance(ctx context.Context, instanceName string) error {
	logger := zap.L().With(zap.String("instance", instanceName))

	instance, err := s.instanceRepo.FindByInstanceName(ctx, instanceName)
	if err != nil {
		return fmt.Errorf("instance not found: %w", err)
	}
	if instance.APIKey == nil {
		return fmt.Errorf("instance %s has no api key configured", instanceName)
	}

	// Passo 1: Deletar instância corrompida na Evolution GO.
	// Usa instanceID (UUID) com o global admin apikey — não o token da instância.
	logger.Info("deleting corrupted instance from Evolution GO", zap.String("instance_id", instance.InstanceID))
	if err := s.evolution.DeleteInstance(ctx, instance.InstanceID); err != nil {
		logger.Error("failed to delete instance from Evolution GO", zap.Error(err))
		return fmt.Errorf("failed to delete evolution instance: %w", err)
	}

	// Passo 2: Recriar com mesmo nome, instanceID e token para preservar configurações.
	logger.Info("recreating instance in Evolution GO")
	recreateReq := models.EvolutionCreateInstanceRequest{
		Name:       instance.InstanceName,
		InstanceID: instance.InstanceID,
		Token:      *instance.APIKey,
		AdvancedSettings: &models.EvolutionAdvancedSettings{
			AlwaysOnline:  true,
			IgnoreGroups:  true,
			IgnoreStatus:  true,
			MsgRejectCall: "This number does not accept calls. Please send a text message.",
			ReadMessages:  true,
			RejectCall:    true,
		},
	}
	if _, err := s.evolution.CreateInstance(ctx, recreateReq); err != nil {
		logger.Error("failed to recreate instance in Evolution GO", zap.Error(err))
		return fmt.Errorf("failed to recreate evolution instance: %w", err)
	}

	// Passo 3: Configurar webhook (ConnectInstance, Immediate:false).
	// Erro non-fatal — a instância foi recriada e o webhook pode ser configurado
	// no próximo TriggerConnect chamado pelo cliente para gerar o QR Code.
	expandedWebhookBase := os.ExpandEnv(s.webhookBaseURL)
	webhookSecret := os.Getenv("WEBHOOK_SECRET_EVOLUTION")
	webhookURL := fmt.Sprintf("%s/webhook/evolution?instance=%s", expandedWebhookBase, instance.InstanceName)
	if webhookSecret != "" {
		webhookURL += "&token=" + webhookSecret
	}
	connectReq := models.EvolutionConnectRequest{
		Immediate:  false,
		WebhookUrl: webhookURL,
		Subscribe:  []string{"MESSAGE", "CONNECTION", "QRCODE"},
	}
	if err := s.evolution.ConnectInstance(ctx, *instance.APIKey, connectReq); err != nil {
		logger.Warn("connect after recreate failed (non-fatal — QR will be generated via TriggerConnect)", zap.Error(err))
	}

	// Passo 4: Atualizar status no banco do Bridge.
	if err := s.instanceRepo.UpdateConnectionState(ctx, instanceName, "active", nil); err != nil {
		logger.Error("failed to update instance state after recreate", zap.Error(err))
		return fmt.Errorf("failed to update instance state: %w", err)
	}

	logger.Info("instance recreated successfully")
	return nil
}

// ForceLogoutInstance força o logout da instância na Evolution GO, atualiza o banco
// para "disconnected" e retorna sem erro mesmo que a Evolution já esteja desconectada.
func (s *AdminService) ForceLogoutInstance(ctx context.Context, instanceName string) error {
	logger := zap.L().With(zap.String("instance", instanceName))

	instance, err := s.instanceRepo.FindByInstanceName(ctx, instanceName)
	if err != nil {
		return fmt.Errorf("instance not found: %w", err)
	}
	if instance.APIKey == nil {
		return fmt.Errorf("instance %s has no api key configured", instanceName)
	}

	if err := s.evolution.LogoutInstance(ctx, *instance.APIKey); err != nil {
		logger.Warn("evolution logout returned error (ignored — instance may already be disconnected)", zap.Error(err))
	}

	if err := s.instanceRepo.UpdateConnectionState(ctx, instanceName, "close", nil); err != nil {
		logger.Error("failed to update instance state to disconnected after logout", zap.Error(err))
		return err
	}

	logger.Info("instance force-logged out successfully")
	return nil
}
