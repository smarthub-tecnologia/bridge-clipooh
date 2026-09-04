package config

import (
	"fmt"
	"os"
	"strconv"
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
}

type ServerConfig struct {
	Port string
}

// ChatwootConfig — a Cartão Pro é a única conta Chatwoot que este bridge serve.
// AccountID/APIToken/WebhookSecret/InternalURL substituem o que antes vinha
// da tabela `tenants` (agora removida); são fixos por deploy, lidos de env vars.
type ChatwootConfig struct {
	AdminAPIURL   string `mapstructure:"admin_api_url"`
	WebhookBase   string `mapstructure:"webhook_base"`
	AccountID     int
	APIToken      string
	WebhookSecret string
	InternalURL   string
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
	// Config fixa da única conta Chatwoot (Cartão Pro) — substituem a antiga
	// tabela `tenants`, removida na migração 018_remove_tenant_multitenancy.
	"CHATWOOT_ACCOUNT_ID",
	"CHATWOOT_API_TOKEN",
	"CHATWOOT_WEBHOOK_SECRET",
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
	// WEBHOOK_BASE_URL é o nome preferido (ex: https://bridge.mobiochat.com) e tem
	// prioridade real sobre CHATWOOT_WEBHOOK_BASE (nome legado, mantido por
	// compatibilidade — normalmente já vem com o path /webhook/evolution embutido).
	// Lido via os.Getenv (não viper.GetString) de propósito: cfg.Chatwoot.WebhookBase
	// já pode ter sido preenchido pelo Unmarshal a partir de CHATWOOT_WEBHOOK_BASE
	// (viper.AutomaticEnv resolve a chave "chatwoot.webhook_base" do config.yaml
	// contra essa env var antes deste ponto), então checar "== \"\" || HasPrefix(\"${\")"
	// aqui nunca detectaria isso — precisa sobrescrever incondicionalmente quando
	// WEBHOOK_BASE_URL estiver presente.
	if v := os.Getenv("WEBHOOK_BASE_URL"); v != "" {
		cfg.Chatwoot.WebhookBase = v
	} else if cfg.Chatwoot.WebhookBase == "" || strings.HasPrefix(cfg.Chatwoot.WebhookBase, "${") {
		// WEBHOOK_BASE_URL ausente — mantém o que o Unmarshal já resolveu
		// (CHATWOOT_WEBHOOK_BASE via AutomaticEnv), ou cai pro literal não
		// expandido do config.yaml quando nenhuma das duas env vars existe.
		cfg.Chatwoot.WebhookBase = viper.GetString("CHATWOOT_WEBHOOK_BASE")
	}

	// Config fixa da única conta Chatwoot (Cartão Pro) — sem tenants, é lida
	// direto das env vars, sem passar pelo config.yaml.
	cfg.Chatwoot.APIToken = os.Getenv("CHATWOOT_API_TOKEN")
	// CHATWOOT_WEBHOOK_SECRET não tem endpoint de rotação em tempo de execução
	// (não há mais tabela de tenant pra persistir um valor atualizado — ver
	// migration 018_remove_tenant_multitenancy). Rotacionar o secret HMAC do
	// webhook Chatwoot é: gerar/copiar o novo valor na inbox do Chatwoot,
	// atualizar esta env var e reiniciar o processo.
	cfg.Chatwoot.WebhookSecret = os.Getenv("CHATWOOT_WEBHOOK_SECRET")
	if v := os.Getenv("CHATWOOT_ACCOUNT_ID"); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid CHATWOOT_ACCOUNT_ID (must be numeric): %w", err)
		}
		cfg.Chatwoot.AccountID = id
	}
	// Tenta CHATWOOT_BASE_URL primeiro (URL pública), cai para CHATWOOT_INTERNAL_URL
	// (Docker) — mesma precedência que o antigo Tenant.ChatwootInternalURL().
	if url := os.Getenv("CHATWOOT_BASE_URL"); url != "" {
		cfg.Chatwoot.InternalURL = url
	} else {
		cfg.Chatwoot.InternalURL = os.Getenv("CHATWOOT_INTERNAL_URL")
	}

	return &cfg, nil
}
