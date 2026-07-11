package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"text/template"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/config"
	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/internal/queue"
	"github.com/linkkotech/bridge/internal/repository"
)

type CircuitBreaker interface {
	Allow() bool
	Success()
	Failure()
}

type QueueJobPayload struct {
	LogID   string                    `json:"log_id"`
	Request models.SendMessageRequest `json:"request"`
}

type NotificationService struct {
	tenantRepo      repository.TenantRepository
	instanceRepo    repository.InstanceRepository
	notifRepo       *repository.NotificationRepository
	evolutionClient *EvolutionClient
	templates       map[string]string
	circuitBreaker  CircuitBreaker
	idempotency     *IdempotencyService
	limiter         *InstanceLimiter
	callbacks       *CallbackService
	config          config.PolicyConfig
	queue           *queue.RedisQueue
}

func NewNotificationService(
	tenantRepo repository.TenantRepository,
	instanceRepo repository.InstanceRepository,
	notifRepo *repository.NotificationRepository,
	evolutionClient *EvolutionClient,
	templates map[string]string,
	limiter *InstanceLimiter,
	callbacks *CallbackService,
	cfg config.PolicyConfig,
	q *queue.RedisQueue,
) *NotificationService {
	return &NotificationService{
		tenantRepo:      tenantRepo,
		instanceRepo:    instanceRepo,
		notifRepo:       notifRepo,
		evolutionClient: evolutionClient,
		templates:       templates,
		limiter:         limiter,
		callbacks:       callbacks,
		config:          cfg,
		queue:           q,
	}
}

func (s *NotificationService) SetCircuitBreaker(cb CircuitBreaker) {
	s.circuitBreaker = cb
}

func (s *NotificationService) SetIdempotencyService(idem *IdempotencyService) {
	s.idempotency = idem
}

func (s *NotificationService) randomDelay() int {
	minMs := s.config.DelayMinMs
	maxMs := s.config.DelayMaxMs
	if maxMs <= minMs {
		if minMs > 0 {
			return minMs
		}
		return 0
	}
	delay := minMs + rand.Intn(maxMs-minMs+1)
	if delay < 0 {
		return 0
	}
	return delay
}

// SendMessage agora atua como o EnqueueMessage
func (s *NotificationService) SendMessage(ctx context.Context, req models.SendMessageRequest, idempotencyKey string) ([]models.NotificationResponse, error) {
	if len(req.To) == 0 {
		return nil, fmt.Errorf("no recipients specified")
	}

	instanceName := req.Instance
	var tenantID string
	if instanceName == "" {
		instance, err := s.instanceRepo.FindDefaultByTenantID(ctx, "")
		if err == nil && instance != nil {
			tenantID = instance.TenantID
			req.Instance = instance.InstanceName // normalize
		}
	} else {
		tenantID, _ = s.instanceRepo.GetTenantIDByInstance(ctx, instanceName)
	}

	var responses []models.NotificationResponse
	content, _ := json.Marshal(req)

	// EventID do caller só é honrado com exatamente 1 destinatário — um único
	// event_id não correlacionaria unicamente múltiplas mensagens em lote (e
	// colidiria como PK duplicada na segunda iteração).
	externalID := ""
	if len(req.To) == 1 {
		externalID = req.EventID
	}

	for _, to := range req.To {
		resp, err := s.notifRepo.Create(ctx, tenantID, idempotencyKey, to, req.Type, content, externalID)
		if err != nil {
			return nil, err
		}

		responses = append(responses, *resp)

		singleReq := req
		singleReq.To = []string{to}

		jobPayload := QueueJobPayload{
			LogID:   resp.NotificationID,
			Request: singleReq,
		}

		payloadBytes, _ := json.Marshal(jobPayload)
		jobID := uuid.New().String()
		job := queue.Job{
			ID:          jobID,
			PayloadJSON: payloadBytes,
			RetriesLeft: 3,
			NextRetryAt: time.Now(),
		}

		if s.queue != nil {
			if err := s.queue.Enqueue(ctx, job); err != nil {
				s.failWithStatus(ctx, resp.NotificationID, tenantID, req.Instance, StatusFailed, err.Error())
			}
		} else {
			// Se a fila não foi injetada (testes), processa sincronicamente
			s.ProcessSendMessage(ctx, singleReq, resp.NotificationID)
		}
	}
	return responses, nil
}

func statusToEventType(status string) string {
	switch status {
	case StatusBlockedCooldown, StatusBlockedRateLimit:
		return "notification.blocked_rate"
	case StatusBlockedCircuitBreaker, StatusFailed:
		return "notification.blocked_plan"
	default:
		return "notification.blocked_plan"
	}
}

func (s *NotificationService) failWithStatus(ctx context.Context, logID, tenantID, instanceName, status, errMsg string) {
	s.notifRepo.UpdateStatus(ctx, logID, status, "", errMsg)
	if s.callbacks != nil {
		go s.callbacks.Notify(context.Background(), CallbackPayload{
			EventType:    statusToEventType(status),
			LogID:        logID,
			TenantID:     tenantID,
			InstanceName: instanceName,
			Status:       status,
			Error:        errMsg,
			SentAt:       time.Now(),
		})
	}
}

// ProcessSendMessage é invocado pelo Worker do Redis
func (s *NotificationService) ProcessSendMessage(ctx context.Context, req models.SendMessageRequest, logID string) error {
	logger := zap.L().With(zap.String("operation", "process_send_message"), zap.String("log_id", logID))

	if len(req.To) == 0 {
		err := fmt.Errorf("no recipient")
		logger.Error("process_send_message failed", zap.Error(err))
		s.failWithStatus(ctx, logID, "", req.Instance, StatusFailed, err.Error())
		return fmt.Errorf("%w: %v", queue.ErrDiscardJob, err)
	}
	to := req.To[0]

	instanceName := req.Instance
	var instanceToken string
	var tenantID string
	var connectedAt *time.Time

	if instanceName == "" {
		instance, err := s.instanceRepo.FindDefaultByTenantID(ctx, "")
		if err != nil {
			err = fmt.Errorf("no default instance: %w", err)
			logger.Error("process_send_message failed", zap.Error(err))
			s.failWithStatus(ctx, logID, "", "", StatusFailed, err.Error())
			return fmt.Errorf("%w: %v", queue.ErrDiscardJob, err)
		}
		instanceName = instance.InstanceName
		tenantID = instance.TenantID
		connectedAt = instance.ConnectedAt
		if instance.APIKey != nil {
			instanceToken = *instance.APIKey
		}
	} else {
		instance, err := s.instanceRepo.FindByInstanceName(ctx, instanceName)
		if err == nil && instance != nil {
			tenantID = instance.TenantID
			connectedAt = instance.ConnectedAt
			if instance.APIKey != nil {
				instanceToken = *instance.APIKey
			}
		} else {
			err = fmt.Errorf("instance not found: %s", instanceName)
			logger.Error("process_send_message failed", zap.Error(err))
			// tenantID is unknown here, so we pass empty string, but logID and instanceName are preserved
			s.failWithStatus(ctx, logID, "", instanceName, StatusFailed, err.Error())
			return fmt.Errorf("%w: %v", queue.ErrDiscardJob, err)
		}
	}

	if connectedAt == nil {
		now := time.Now()
		connectedAt = &now
	}

	// Policy Engine Checks
	if s.limiter != nil {
		if allowed, err := s.limiter.AllowRecipientSend(ctx, instanceName, to); err != nil || !allowed {
			logger.Error("process_send_message failed", zap.Error(ErrCooldownActive))
			s.failWithStatus(ctx, logID, tenantID, instanceName, StatusBlockedCooldown, ErrCooldownActive.Error())
			return fmt.Errorf("%w: %v", queue.ErrDiscardJob, ErrCooldownActive)
		}

		if allowed, err := s.limiter.AllowInstanceSend(ctx, instanceName, *connectedAt); err != nil || !allowed {
			logger.Error("process_send_message failed", zap.Error(ErrRateLimitExceeded))
			s.failWithStatus(ctx, logID, tenantID, instanceName, StatusBlockedRateLimit, ErrRateLimitExceeded.Error())
			return fmt.Errorf("%w: %v", queue.ErrDiscardJob, ErrRateLimitExceeded)
		}
	}

	if s.circuitBreaker != nil && !s.circuitBreaker.Allow() {
		logger.Error("process_send_message failed", zap.Error(ErrCircuitBreakerOpen))
		s.failWithStatus(ctx, logID, tenantID, instanceName, StatusBlockedCircuitBreaker, ErrCircuitBreakerOpen.Error())
		return fmt.Errorf("%w: %v", queue.ErrDiscardJob, ErrCircuitBreakerOpen)
	}

	// Envia via Evolution
	delay := s.randomDelay()
	var result *models.EvolutionSendResponse
	var sendErr error

	if req.MediaURL != "" {
		mediaReq := models.EvolutionSendMediaRequest{
			Number:   to,
			URL:      req.MediaURL,
			Type:     req.Type,
			Filename: req.FileName,
			Delay:    delay,
		}
		result, sendErr = s.evolutionClient.SendMedia(ctx, instanceToken, instanceName, mediaReq)
	} else {
		textReq := models.EvolutionSendTextRequest{
			Number: to,
			Text:   req.Text,
			Delay:  delay,
		}
		result, sendErr = s.evolutionClient.SendText(ctx, instanceToken, instanceName, textReq)
	}

	if sendErr != nil {
		if s.circuitBreaker != nil {
			s.circuitBreaker.Failure()
		}
		logger.Error("failed to send message", zap.Error(sendErr), zap.String("to", to))
		s.failWithStatus(ctx, logID, tenantID, instanceName, StatusFailed, sendErr.Error())
		return sendErr
	}

	if s.circuitBreaker != nil {
		s.circuitBreaker.Success()
	}

	s.notifRepo.UpdateStatus(ctx, logID, StatusSent, result.MessageID, "")
	if s.callbacks != nil {
		go s.callbacks.Notify(context.Background(), CallbackPayload{
			EventType:    "notification.sent",
			LogID:        logID,
			TenantID:     tenantID,
			InstanceName: instanceName,
			Status:       StatusSent,
			PhoneNumber:  to,
			MsgID:        result.MessageID,
			SentAt:       time.Now(),
		})
	}

	return nil
}

// SendTemplate renderiza um template e envia
func (s *NotificationService) SendTemplate(ctx context.Context, req models.SendTemplateRequest, idempotencyKey string) ([]models.NotificationResponse, error) {
	tmplStr, ok := s.templates[req.TemplateID]
	if !ok {
		return nil, fmt.Errorf("template '%s' not found", req.TemplateID)
	}
	tmpl, err := template.New("msg").Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, req.Variables); err != nil {
		return nil, fmt.Errorf("failed to render template: %w", err)
	}

	sendReq := models.SendMessageRequest{
		To:       req.To,
		Type:     "text",
		Text:     buf.String(),
		Instance: req.Instance,
		Metadata: req.Metadata,
	}
	return s.SendMessage(ctx, sendReq, idempotencyKey)
}

// GetStatus consulta status de uma notificação
func (s *NotificationService) GetStatus(ctx context.Context, id string) (*models.NotificationStatusResponse, error) {
	return s.notifRepo.FindByIDOrKey(ctx, id)
}

// ListInstances lista instâncias disponíveis
func (s *NotificationService) ListInstances(ctx context.Context) ([]models.InstanceInfo, error) {
	instances, err := s.instanceRepo.FindAll(ctx)
	if err != nil {
		return nil, err
	}
	var result []models.InstanceInfo
	for _, inst := range instances {
		phone := ""
		if inst.PhoneNumber != nil {
			phone = *inst.PhoneNumber
		}
		result = append(result, models.InstanceInfo{
			InstanceName: inst.InstanceName,
			Status:       inst.Status,
			PhoneNumber:  phone,
		})
	}
	return result, nil
}
