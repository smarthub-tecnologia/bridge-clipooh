package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/config"
	"github.com/linkkotech/bridge/internal/handlers"
	"github.com/linkkotech/bridge/internal/middleware"
	"github.com/linkkotech/bridge/internal/observability"
	"github.com/linkkotech/bridge/internal/queue"
	"github.com/linkkotech/bridge/internal/repository"
	"github.com/linkkotech/bridge/internal/services"
	"github.com/linkkotech/bridge/pkg/metaconfig"
)

func runMigrations(databaseURL string) {
	m, err := migrate.New("file://migrations", databaseURL)
	if err != nil {
		zap.L().Fatal("failed to initialize migrations", zap.Error(err))
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		zap.L().Fatal("migration failed", zap.Error(err))
	}
	zap.L().Info("migrations applied successfully")
}

func main() {
	// Logger
	logger, _ := zap.NewProduction()
	zap.ReplaceGlobals(logger)
	defer logger.Sync()

	// Valida variáveis de ambiente antes de qualquer inicialização
	warnings, err := config.ValidateEnv()
	if err != nil {
		// Extrai o nome da variável da mensagem para structured logging
		varName := strings.TrimPrefix(err.Error(), "missing required env var: ")
		logger.Fatal("missing required env var",
			zap.String("var", varName),
			zap.String("action", "set this env var and restart"),
		)
	}
	for _, w := range warnings {
		logger.Warn("missing optional env var — some features will be unavailable",
			zap.String("var", w),
		)
	}

	// Carrega configuração
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	// Contexto raiz — cancelado no shutdown para parar goroutines de background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Conexão com banco
	pool, err := config.NewDatabasePool(ctx)
	if err != nil {
		logger.Fatal("failed to connect to database", zap.Error(err))
	}
	defer pool.Close()

	// Conexão com banco do Directus (opcional)
	directusPool, err := config.NewDirectusDatabasePool(ctx)
	if err != nil {
		logger.Warn("failed to connect to directus database, addon_events will not be recorded", zap.Error(err))
	} else {
		defer directusPool.Close()
	}

	// Conexão com Supabase (opcional — meta_provider_configs).
	// Quando presente, GetMetaConfig usa SQL direto em vez da REST do Directus.
	// Se SUPABASE_DATABASE_URL estiver ausente, o Bridge usa Directus REST como fallback.
	if os.Getenv("SUPABASE_DATABASE_URL") != "" {
		supabasePool, spErr := config.NewSupabaseDatabasePool(ctx)
		if spErr != nil {
			logger.Warn("failed to connect to supabase, metaconfig will use directus rest",
				zap.Error(spErr),
			)
		} else {
			defer supabasePool.Close()
			metaconfig.SetSupabasePool(supabasePool)
			logger.Info("metaconfig: supabase pool active, using direct SQL for meta_provider_configs")
		}
	}

	// Roda as migrações (se DATABASE_URL não vier da config, pode pegar do env)
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// Apenas para caso a config tenha
		// dbURL = cfg.Database.URL
		// Mas no WA-bridge nós não mapeamos database URL no struct, então pegamos do env
	}
	if dbURL != "" {
		runMigrations(dbURL)
	}

	// Repositórios
	tenantRepo := repository.NewTenantRepository(pool)
	instanceRepo := repository.NewInstanceRepository(pool)
	inboxRepo := repository.NewInboxRepository(pool)
	eventRepo := repository.NewAddonEventRepository(directusPool)

	// Clientes externos
	evolutionClient := services.NewEvolutionClient(
		cfg.Evolution.BaseURL,
		cfg.Evolution.APIKey,
	)
	chatwootAdmin := services.NewChatwootAdminClient(
		cfg.Chatwoot.AdminAPIURL,
		os.Getenv("CHATWOOT_PLATFORM_API_TOKEN"), // Token de Platform App
	)

	// MediaService
	mediaService := services.NewMediaService("/tmp/wa-bridge")

	// BridgeService
	bridgeService := services.NewBridgeService(
		tenantRepo,
		instanceRepo,
		inboxRepo,
		eventRepo,
		evolutionClient,
		mediaService,
	)

	// Services
	adminService := services.NewAdminService(
		tenantRepo,
		instanceRepo,
		inboxRepo,
		pool,
		chatwootAdmin,
		evolutionClient,
		cfg.Chatwoot.WebhookBase,
	)

	// Redis
	redisClient := config.NewRedisClient()
	defer redisClient.Close()

	cacheService := services.NewCacheService(redisClient)

	// Idempotency
	idempotencyService := services.NewIdempotencyService(redisClient)

	// Circuit Breaker para Evolution
	evolutionCB := queue.NewRedisCircuitBreaker(redisClient, "evolution", 30*time.Second, 5)

	// Instance Limiter & Callbacks
	instanceLimiter := services.NewInstanceLimiter(redisClient, cfg.Policy)
	callbackService := services.NewCallbackService(cfg.Callbacks)

	// Notification Repo & Service
	notifRepo := repository.NewNotificationRepository(pool)

	var templates map[string]string
	if cfg.Templates != nil {
		templates = cfg.Templates
	}

	// Fila de Notificações
	msgQueue := queue.NewRedisQueue(redisClient, "wa-bridge:jobs")

	notificationService := services.NewNotificationService(
		tenantRepo,
		instanceRepo,
		notifRepo,
		evolutionClient,
		templates,
		instanceLimiter,
		callbackService,
		cfg.Policy,
		msgQueue,
	)
	notificationService.SetCircuitBreaker(evolutionCB)
	notificationService.SetIdempotencyService(idempotencyService)

	workerConcurrency := cfg.Queue.WorkerConcurrency
	if workerConcurrency <= 0 {
		workerConcurrency = 1
	}

	workerFunc := func(job queue.Job) error {
		var payload services.QueueJobPayload
		if err := json.Unmarshal(job.PayloadJSON, &payload); err != nil {
			return err
		}
		return notificationService.ProcessSendMessage(ctx, payload.Request, payload.LogID)
	}

	for i := 0; i < workerConcurrency; i++ {
		go msgQueue.Consume(ctx, workerFunc)
	}
	go msgQueue.ProcessRetries(ctx, workerFunc)

	bridgeService.SetCacheService(cacheService)
	bridgeService.SetCallbackService(callbackService)
	adminService.SetCallbackService(callbackService)

	// Métricas
	metrics := observability.New()
	bridgeService.SetMetrics(metrics)

	// Auth
	authService := middleware.NewAuthService()

	// Rate Limiter
	rateLimiter := middleware.NewRateLimiter(redisClient)

	// Handlers
	adminHandler := handlers.NewAdminHandler(adminService, authService)
	evolutionHandler := handlers.NewEvolutionHandler(bridgeService, authService)
	evolutionHandler.SetMetrics(metrics)
	chatwootHandler := handlers.NewChatwootHandler(bridgeService, authService)
	chatwootHandler.SetMetrics(metrics)
	notifyHandler := handlers.NewNotifyHandler(notificationService, authService)
	notifyHandler.SetCacheService(cacheService)
	notifyHandler.SetAdminService(adminService)
	healthHandler := handlers.NewHealthHandler(pool, redisClient, cfg.Evolution.BaseURL)
	metricsHandler := handlers.NewMetricsHandler(metrics, pool, redisClient)

	// Router
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(middleware.LoggerMiddleware)
	r.Use(chimiddleware.Recoverer)
	r.Use(rateLimiter.LimitByIP(100, 1*time.Minute))

	tenantRateLimit := cfg.Policy.TenantRateLimit
	if tenantRateLimit <= 0 {
		tenantRateLimit = 60
	}
	r.Use(rateLimiter.LimitByTenant(tenantRateLimit, 1*time.Minute))

	// Health
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	r.Get("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","service":"wa-bridge","version":"0.4.0"}`))
	})

	// Liveness & Readiness (públicos — sem auth, EasyPanel precisa acessar)
	r.Get("/health/live", healthHandler.Live)
	r.Get("/health/ready", healthHandler.Ready)

	// Webhooks
	r.Post("/webhook/evolution", evolutionHandler.HandleWebhook)
	r.Post("/webhook/chatwoot", chatwootHandler.HandleWebhook)

	// Admin API (protegida por ADMIN_API_KEY)
	r.Route("/api/v1/admin", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !authService.ValidateAdminAPIKey(r) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					w.Write([]byte(`{"error":"unauthorized","code":"INVALID_API_KEY"}`))
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		r.Post("/tenants", adminHandler.CreateTenant)
		r.Get("/tenants/{id}", adminHandler.GetTenant)
		r.Delete("/tenants/{id}", adminHandler.DeleteTenant)
		r.Post("/tenants/{id}/connect", adminHandler.ConnectTenant)
		r.Put("/tenants/{id}/chatwoot-webhook-secret", adminHandler.SetChatwootWebhookSecret)
		r.Post("/tenants/{id}/sync-chatwoot-secret", adminHandler.SyncChatwootSecret)
		r.Post("/tenants/{id}/chatwoot-widget-inbox", adminHandler.CreateWidgetInbox)
		r.Post("/tenants/{id}/chatwoot-widget-webhook/sync", adminHandler.SyncWidgetWebhook)
		r.Post("/tenants/{id}/agents", adminHandler.CreateAgent)
		r.Delete("/tenants/{id}/agents/{agentId}", adminHandler.RemoveAgent)
		r.Get("/metrics", metricsHandler.GetMetrics)
	})

	// Notification API (Flow C)
	r.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.BridgeAPIKeyAuth(authService))
			r.Post("/notify/send", notifyHandler.SendMessage)
			r.Post("/notify/template", notifyHandler.SendTemplate)
			r.Get("/notify/status/{id}", notifyHandler.GetStatus)
			r.Get("/instances", notifyHandler.ListInstances)
			r.Get("/instances/{instanceName}/qr", notifyHandler.GetQRCode)
			r.Post("/instances/{instanceName}/connect", notifyHandler.TriggerConnect)
			r.Post("/instances/{instanceName}/recreate", notifyHandler.RecreateInstance)
			r.Delete("/instances/{instanceName}/logout", notifyHandler.LogoutInstance)
		})

		// Meta webhook endpoints are intentionally outside BridgeAPIKeyAuth.
		// Authentication is handled by META_WEBHOOK_VERIFY_TOKEN (GET)
		// and by the implicit trust of Meta's webhook delivery (POST).
		metaWebhookHandler := handlers.NewMetaWebhookHandler()
		r.Get("/meta/webhook", metaWebhookHandler.VerifyWebhook)
		r.Post("/meta/webhook", metaWebhookHandler.HandleEvent)
	})

	// Servidor com graceful shutdown
	port := cfg.Server.Port
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", port),
		Handler: r,
	}

	// Webhook health checker — intervalo configurável via WEBHOOK_HEALTH_CHECK_INTERVAL (padrão: 10m)
	healthCheckInterval := 10 * time.Minute
	if v := os.Getenv("WEBHOOK_HEALTH_CHECK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			healthCheckInterval = d
		}
	}
	go adminService.RunWebhookHealthChecker(ctx, healthCheckInterval)

	go func() {
		logger.Info("wa-bridge starting", zap.String("port", port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server failed", zap.Error(err))
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutting down server...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Fatal("server forced to shutdown", zap.Error(err))
	}
	logger.Info("server exited")
}
