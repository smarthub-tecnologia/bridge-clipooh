package metaconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/linkkotech/bridge/pkg/cipher"
)

var ErrConfigNotFound = errors.New("meta_config_not_found")

type MetaConfig struct {
	PhoneNumberID string
	AccessToken   string // descriptografado, apenas em memória
}

type cacheEntry struct {
	config    *MetaConfig
	fetchedAt time.Time
}

var cache sync.Map // key: instanceID (string) → cacheEntry

// phoneNumberCache: key: phoneNumberID (string) → phoneNumberCacheEntry
type phoneNumberCacheEntry struct {
	instanceID string
	fetchedAt  time.Time
}

var phoneNumberCache sync.Map

const cacheTTL = 5 * time.Minute

var httpClient = &http.Client{Timeout: 5 * time.Second}

// decryptFn permite substituição em testes sem modificar pkg/cipher.
var decryptFn = cipher.DecryptToken

type phoneNumberRow struct {
	EvolutionInstanceID string `json:"evolution_instance_id"`
}

type phoneNumberResponse struct {
	Data []phoneNumberRow `json:"data"`
}

type waMessageLogRow struct {
	EventID string `json:"event_id"`
}

type waMessageLogResponse struct {
	Data []waMessageLogRow `json:"data"`
}

type evolutionInstanceRow struct {
	ID string `json:"id"`
}

type evolutionInstanceResponse struct {
	Data []evolutionInstanceRow `json:"data"`
}

// resolveInstanceUUID resolve o nome legível da instância Evolution (ex.:
// "wa-xxx", o mesmo valor de webhook.InstanceName / meta_provider_configs.
// evolution_instance_id, ambos varchar) pro id interno (UUID) de
// evolution_instances — necessário porque wa_message_logs.evolution_instance_id
// é FK UUID, não a string legível.
//
// Prefere supabasePool (SQL direto) quando configurado — mesmo padrão de
// GetMetaConfig/GetInstanceIDByPhoneNumberID. Directus REST é fallback (e
// depende de DIRECTUS_URL estar configurada no ambiente, o que nem sempre é
// o caso — foi exatamente isso que quebrou silenciosamente aqui).
func resolveInstanceUUID(instanceID string) (string, bool, error) {
	if supabasePool != nil {
		return resolveInstanceUUIDFromSupabase(instanceID)
	}
	return resolveInstanceUUIDViaDirectus(instanceID)
}

func resolveInstanceUUIDViaDirectus(instanceID string) (string, bool, error) {
	url := fmt.Sprintf(
		"%s/items/evolution_instances"+
			"?filter[evolution_instance_id][_eq]=%s"+
			"&fields=id"+
			"&limit=1",
		os.Getenv("DIRECTUS_URL"), instanceID,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", false, fmt.Errorf("metaconfig: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("DIRECTUS_SERVICE_TOKEN"))

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("metaconfig: directus request: %w", err)
	}
	defer resp.Body.Close()

	var body evolutionInstanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, fmt.Errorf("metaconfig: decode response: %w", err)
	}

	if len(body.Data) == 0 {
		return "", false, nil
	}
	return body.Data[0].ID, true, nil
}

type directusRow struct {
	PhoneNumberID   string `json:"wa_phone_number_id"`
	AccessTokenEnc  string `json:"wa_access_token_enc"`
	TokenKeyVersion string `json:"token_key_version"`
}

type directusResponse struct {
	Data []directusRow `json:"data"`
}

// LookupEventIDByPhone returns the event_id of the most recent sent or delivered
// message to the given phone number on the given Evolution instance.
//
// instanceID é o nome legível da instância (webhook.InstanceName /
// meta_provider_configs.evolution_instance_id) — resolvido internamente pro
// UUID de evolution_instances.id antes de filtrar wa_message_logs (ver
// resolveInstanceUUID; sem essa resolução o filtro nunca batia, pra nenhum
// dos dois providers).
//
// Returns ("", false, nil) when no matching wa_message_log is found — the
// caller should dispatch a message.received callback with event_id: null.
// Returns a non-nil error only on network or decode failure.
// No caching: wa_message_logs are write-heavy and must always be fresh.
func LookupEventIDByPhone(instanceID, phone string) (string, bool, error) {
	resolvedInstanceID, found, err := resolveInstanceUUID(instanceID)
	if err != nil {
		return "", false, fmt.Errorf("metaconfig: resolve instance uuid: %w", err)
	}
	if !found {
		return "", false, nil
	}

	if supabasePool != nil {
		return lookupEventIDByPhoneFromSupabase(resolvedInstanceID, phone)
	}
	return lookupEventIDByPhoneViaDirectus(resolvedInstanceID, phone)
}

func lookupEventIDByPhoneViaDirectus(resolvedInstanceID, phone string) (string, bool, error) {
	url := fmt.Sprintf(
		"%s/items/wa_message_logs"+
			"?filter[evolution_instance_id][_eq]=%s"+
			"&filter[recipient_phone][_eq]=%s"+
			"&filter[status][_in]=sent,delivered"+
			"&sort=-sent_at"+
			"&fields=event_id"+
			"&limit=1",
		os.Getenv("DIRECTUS_URL"), resolvedInstanceID, phone,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", false, fmt.Errorf("metaconfig: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("DIRECTUS_SERVICE_TOKEN"))

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("metaconfig: directus request: %w", err)
	}
	defer resp.Body.Close()

	var body waMessageLogResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, fmt.Errorf("metaconfig: decode response: %w", err)
	}

	if len(body.Data) == 0 {
		return "", false, nil
	}
	return body.Data[0].EventID, true, nil
}

// GetMetaConfig retorna as credenciais Meta Cloud API para a instância Evolution
// informada. Na primeira chamada busca as credenciais (Supabase SQL quando configurado,
// Directus REST como fallback) e descriptografa o token;
// chamadas subsequentes dentro de 5 minutos são servidas do cache.
//
// Retorna ErrConfigNotFound quando a instância não tem config Meta ativa.
// Outros erros indicam falha de rede ou de decriptação — o caller deve tratar
// de forma diferente de ErrConfigNotFound.
func GetMetaConfig(instanceID string) (*MetaConfig, error) {
	if v, ok := cache.Load(instanceID); ok {
		if e := v.(cacheEntry); time.Since(e.fetchedAt) < cacheTTL {
			return e.config, nil
		}
	}

	// Supabase path: query SQL direta — evita round-trip REST + overhead do Directus.
	// Fallback para REST quando SUPABASE_DATABASE_URL não está configurada (supabasePool == nil).
	if supabasePool != nil {
		cfg, err := fetchFromSupabase(instanceID)
		if err == nil {
			cache.Store(instanceID, cacheEntry{config: cfg, fetchedAt: time.Now()})
			return cfg, nil
		}
		// Propaga tanto ErrConfigNotFound quanto erros de rede/decriptação.
		// Não há fallback para Directus REST quando o pool está ativo — Supabase é a fonte de verdade.
		return nil, err
	}

	url := fmt.Sprintf(
		"%s/items/meta_provider_configs"+
			"?filter[evolution_instance_id][_eq]=%s"+
			"&filter[is_active][_eq]=true"+
			"&fields=wa_phone_number_id,wa_access_token_enc,token_key_version"+
			"&limit=1",
		os.Getenv("DIRECTUS_URL"), instanceID,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("metaconfig: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("DIRECTUS_SERVICE_TOKEN"))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metaconfig: directus request: %w", err)
	}
	defer resp.Body.Close()

	var body directusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("metaconfig: decode response: %w", err)
	}

	if len(body.Data) == 0 {
		return nil, ErrConfigNotFound
	}

	row := body.Data[0]
	token, err := decryptFn(row.AccessTokenEnc, row.TokenKeyVersion)
	if err != nil {
		return nil, fmt.Errorf("metaconfig: decrypt token: %w", err)
	}

	cfg := &MetaConfig{PhoneNumberID: row.PhoneNumberID, AccessToken: token}
	cache.Store(instanceID, cacheEntry{config: cfg, fetchedAt: time.Now()})
	return cfg, nil
}

// GetInstanceIDByPhoneNumberID resolves a Meta phone_number_id to the
// Evolution instance ID. Uses Supabase SQL when configured, Directus REST as fallback.
//
// Results are cached for 5 minutes. Returns ErrConfigNotFound when no active
// config exists for the given phone number ID.
func GetInstanceIDByPhoneNumberID(phoneNumberID string) (string, error) {
	if v, ok := phoneNumberCache.Load(phoneNumberID); ok {
		if e := v.(phoneNumberCacheEntry); time.Since(e.fetchedAt) < cacheTTL {
			return e.instanceID, nil
		}
	}

	// Supabase path — fallback para Directus REST quando pool não configurado.
	if supabasePool != nil {
		instanceID, err := fetchInstanceIDByPhoneFromSupabase(phoneNumberID)
		if err == nil {
			phoneNumberCache.Store(phoneNumberID, phoneNumberCacheEntry{
				instanceID: instanceID,
				fetchedAt:  time.Now(),
			})
			return instanceID, nil
		}
		return "", err
	}

	url := fmt.Sprintf(
		"%s/items/meta_provider_configs"+
			"?filter[wa_phone_number_id][_eq]=%s"+
			"&filter[is_active][_eq]=true"+
			"&fields=evolution_instance_id"+
			"&limit=1",
		os.Getenv("DIRECTUS_URL"), phoneNumberID,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("metaconfig: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("DIRECTUS_SERVICE_TOKEN"))

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("metaconfig: directus request: %w", err)
	}
	defer resp.Body.Close()

	var body phoneNumberResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("metaconfig: decode response: %w", err)
	}

	if len(body.Data) == 0 {
		return "", ErrConfigNotFound
	}

	instanceID := body.Data[0].EvolutionInstanceID
	phoneNumberCache.Store(phoneNumberID, phoneNumberCacheEntry{
		instanceID: instanceID,
		fetchedAt:  time.Now(),
	})
	return instanceID, nil
}
