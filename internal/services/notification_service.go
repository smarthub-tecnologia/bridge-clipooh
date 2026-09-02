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
	instanceRepo    repository.InstanceRepository
	notifRepo       *repository.NotificationRepository
	evolutionClient *EvolutionClient
	templates       map[string]string
	circuitBreaker  CircuitBreaker
	idempotency     *IdempotencyService
	limiter         *InstanceLimiter
	config          config.PolicyConfig
	queue           *queue.RedisQueue
}

func NewNotificationService(
	instanceRepo repository.InstanceRepository,
	notifRepo *repository.NotificationRepository,
	evolutionClient *EvolutionClient,
	templates map[string]string,
	limiter *InstanceLimiter,
	cfg config.PolicyConfig,
	q *queue.RedisQueue,
) *NotificationService {
	return &NotificationService{
		instanceRepo:    instanceRepo,
		notifRepo:       notifRepo,
		evolutionClient: evolutionClient,
		templates:       templates,
		limiter:         limiter,
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

	if req.Instance == "" {
		if instance, err := s.instanceRepo.FindDefault(ctx); err == nil && instance != nil {
			req.Instance = instance.InstanceName // normalize
		}
	}
	instanceName := req.Instance

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
		resp, err := s.notifRepo.Create(ctx, idempotencyKey, to, req.Type, content, externalID, instanceName)
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
				s.failWithStatus(ctx, resp.NotificationID, req.Instance, StatusFailed, err.Error())
			}
		} else {
			// Se a fila não foi injetada (testes), processa sincronicamente
			s.ProcessSendMessage(ctx, singleReq, resp.NotificationID)
		}
	}
	return responses, nil
}

func (s *NotificationService) failWithStatus(ctx context.Context, logID, instanceName, status, errMsg string) {
	s.notifRepo.UpdateStatus(ctx, logID, status, "", errMsg)
}

// ProcessSendMessage é invocado pelo Worker do Redis
func (s *NotificationService) ProcessSendMessage(ctx context.Context, req models.SendMessageRequest, logID string) error {
	logger := zap.L().With(zap.String("operation", "process_send_message"), zap.String("log_id", logID))

	if len(req.To) == 0 {
		err := fmt.Errorf("no recipient")
		logger.Error("process_send_message failed", zap.Error(err))
		s.failWithStatus(ctx, logID, req.Instance, StatusFailed, err.Error())
		return fmt.Errorf("%w: %v", queue.ErrDiscardJob, err)
	}
	to := req.To[0]

	instanceName := req.Instance
	var instanceToken string
	var connectedAt *time.Time

	if instanceName == "" {
		instance, err := s.instanceRepo.FindDefault(ctx)
		if err != nil {
			err = fmt.Errorf("no default instance: %w", err)
			logger.Error("process_send_message failed", zap.Error(err))
			s.failWithStatus(ctx, logID, "", StatusFailed, err.Error())
			return fmt.Errorf("%w: %v", queue.ErrDiscardJob, err)
		}
		instanceName = instance.InstanceName
		connectedAt = instance.ConnectedAt
		if instance.APIKey != nil {
			instanceToken = *instance.APIKey
		}
	} else {
		instance, err := s.instanceRepo.FindByInstanceName(ctx, instanceName)
		if err == nil && instance != nil {
			connectedAt = instance.ConnectedAt
			if instance.APIKey != nil {
				instanceToken = *instance.APIKey
			}
		} else {
			err = fmt.Errorf("instance not found: %s", instanceName)
			logger.Error("process_send_message failed", zap.Error(err))
			s.failWithStatus(ctx, logID, instanceName, StatusFailed, err.Error())
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
			s.failWithStatus(ctx, logID, instanceName, StatusBlockedCooldown, ErrCooldownActive.Error())
			return fmt.Errorf("%w: %v", queue.ErrDiscardJob, ErrCooldownActive)
		}

		if allowed, err := s.limiter.AllowInstanceSend(ctx, instanceName, *connectedAt); err != nil || !allowed {
			logger.Error("process_send_message failed", zap.Error(ErrRateLimitExceeded))
			s.failWithStatus(ctx, logID, instanceName, StatusBlockedRateLimit, ErrRateLimitExceeded.Error())
			return fmt.Errorf("%w: %v", queue.ErrDiscardJob, ErrRateLimitExceeded)
		}
	}

	if s.circuitBreaker != nil && !s.circuitBreaker.Allow() {
		logger.Error("process_send_message failed", zap.Error(ErrCircuitBreakerOpen))
		s.failWithStatus(ctx, logID, instanceName, StatusBlockedCircuitBreaker, ErrCircuitBreakerOpen.Error())
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
		s.failWithStatus(ctx, logID, instanceName, StatusFailed, sendErr.Error())
		return sendErr
	}

	if s.circuitBreaker != nil {
		s.circuitBreaker.Success()
	}

	s.notifRepo.UpdateStatus(ctx, logID, StatusSent, result.MessageID, "")

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
