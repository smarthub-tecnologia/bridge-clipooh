package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server    ServerConfig
	Chatwoot  ChatwootConfig
	Evolution EvolutionConfig
	Templates map[string]string `mapstructure:"templates"`
	Queue     QueueConfig       `mapstructure:"queue"`
	Policy    PolicyConfig      `mapstructure:"policy"`
	Callbacks CallbacksConfig   `mapstructure:"callbacks"`
}

type QueueConfig struct {
	WorkerConcurrency int `mapstructure:"worker_concurrency"`
}

type PolicyConfig struct {
	DelayMinMs               int `mapstructure:"delay_min_ms"`
	DelayMaxMs               int `mapstructure:"delay_max_ms"`
	DefaultMaxMessagesPerDay int `mapstructure:"default_max_messages_per_day"`
	WarmupDays               int `mapstructure:"warmup_days"`
	WarmupMaxMessagesPerDay  int `mapstructure:"warmup_max_messages_per_day"`
	RecipientCooldownSeconds int `mapstructure:"recipient_cooldown_seconds"`
	TenantRateLimit          int `mapstructure:"tenant_rate_limit"`
}

type CallbacksConfig struct {
	DirectusWebhookURL    string `mapstructure:"directus_webhook_url"`
	DirectusWebhookSecret string `mapstructure:"directus_webhook_secret"`
	Enabled               bool   `mapstructure:"enabled"`
}

type ServerConfig struct {
	Port string
}

type ChatwootConfig struct {
	AdminAPIURL string `mapstructure:"admin_api_url"`
	WebhookBase string `mapstructure:"webhook_base"`
}

type EvolutionConfig struct {
	BaseURL string `mapstructure:"base_url"`
	APIKey  string `mapstructure:"api_key"`
}

// RequiredVars são as variáveis de ambiente sem as quais o serviço não pode operar.
var RequiredVars = []string{
	"DATABASE_URL",
	"REDIS_URL",
	"EVOLUTION_BASE_URL",
	"ADMIN_API_KEY",
	"BRIDGE_API_KEY",
}

// WarnVars são variáveis ausentes que degradam funcionalidade mas não impedem o boot.
var WarnVars = []string{
	"WEBHOOK_BASE_URL",
	"CHATWOOT_ADMIN_EMAIL",
	"CHATWOOT_PLATFORM_API_TOKEN",
	"EVOLUTION_API_KEY",
	// URL pública do app Next.js — usada só pra registrar o webhook de conta
	// do addon "Widget de Chat" (Fase B). Ausente = CreateWidgetInbox segue
	// funcionando, só sem tempo real (ver AdminService.ensureWidgetWebhook).
	"NEXTJS_APP_URL",
}

// ValidateEnv verifica variáveis obrigatórias e retorna erro na primeira ausente.
// Também valida que ao menos uma das URLs do Chatwoot está presente.
// Warnings são retornados separadamente — o caller decide como logar.
func ValidateEnv() (warnings []string, err error) {
	for _, v := range RequiredVars {
		if os.Getenv(v) == "" {
			return nil, fmt.Errorf("missing required env var: %s", v)
		}
	}

	// Chatwoot URL: aceita CHATWOOT_BASE_URL ou CHATWOOT_INTERNAL_URL
	if os.Getenv("CHATWOOT_BASE_URL") == "" && os.Getenv("CHATWOOT_INTERNAL_URL") == "" {
		return nil, fmt.Errorf("missing required env var: CHATWOOT_BASE_URL (or CHATWOOT_INTERNAL_URL)")
	}

	for _, v := range WarnVars {
		if os.Getenv(v) == "" {
			warnings = append(warnings, v)
		}
	}
	return warnings, nil
}

func LoadConfig(path string) (*Config, error) {
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")

	// Read environment variables
	viper.AutomaticEnv()
	// Allow overriding nested keys, e.g. CHATWOOT_ADMIN_API_URL instead of CHATWOOT.ADMIN_API_URL
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))

	// Bind env vars explicitly to allow viper.Unmarshal to read them for callbacks
	_ = viper.BindEnv("callbacks.directus_webhook_url", "CALLBACKS_DIRECTUS_WEBHOOK_URL")
	_ = viper.BindEnv("callbacks.directus_webhook_secret", "CALLBACKS_DIRECTUS_WEBHOOK_SECRET")
	_ = viper.BindEnv("callbacks.enabled", "CALLBACKS_ENABLED")

	viper.SetDefault("policy.default_max_messages_per_day", 200)
	viper.SetDefault("policy.warmup_max_messages_per_day", 40)
	viper.SetDefault("policy.warmup_days", 7)
	viper.SetDefault("policy.delay_min_ms", 3000)
	viper.SetDefault("policy.delay_max_ms", 8000)
	viper.SetDefault("policy.recipient_cooldown_seconds", 300)

	if err := viper.ReadInConfig(); err != nil {
		// Ignore if config.yaml is not found, fallback to env vars
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	// Since we might be using pure env vars directly without yaml mappings in some cases:
	if port := os.Getenv("PORT"); port != "" {
		cfg.Server.Port = port
	} else if cfg.Server.Port == "" || strings.HasPrefix(cfg.Server.Port, "${") {
		cfg.Server.Port = "8080"
	}
	if cfg.Evolution.BaseURL == "" {
		cfg.Evolution.BaseURL = viper.GetString("EVOLUTION_BASE_URL")
	}
	if cfg.Evolution.APIKey == "" {
		cfg.Evolution.APIKey = viper.GetString("EVOLUTION_API_KEY")
	}
	if cfg.Chatwoot.AdminAPIURL == "" {
		cfg.Chatwoot.AdminAPIURL = viper.GetString("CHATWOOT_ADMIN_API_URL")
	}
	if cfg.Chatwoot.WebhookBase == "" || strings.HasPrefix(cfg.Chatwoot.WebhookBase, "${") {
		// WEBHOOK_BASE_URL é o nome preferido (ex: https://bridge.mobiochat.com)
		cfg.Chatwoot.WebhookBase = viper.GetString("WEBHOOK_BASE_URL")
	}
	if cfg.Chatwoot.WebhookBase == "" || strings.HasPrefix(cfg.Chatwoot.WebhookBase, "${") {
		// Fallback para o nome antigo
		cfg.Chatwoot.WebhookBase = viper.GetString("CHATWOOT_WEBHOOK_BASE")
	}

	return &cfg, nil
}
