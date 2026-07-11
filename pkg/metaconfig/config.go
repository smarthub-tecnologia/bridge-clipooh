package metaconfig

import (
	"errors"
	"fmt"
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

// decryptFn permite substituição em testes sem modificar pkg/cipher.
var decryptFn = cipher.DecryptToken

// GetMetaConfig retorna as credenciais Meta Cloud API para a instância Evolution
// informada (instanceID = nome legível da instância, ex.: "wa-xxx" — o mesmo
// valor de evolution_instances.instance_name e do ?instance= dos webhooks).
//
// Primeira chamada consulta meta_provider_configs e descriptografa o token;
// chamadas subsequentes dentro de 5 minutos são servidas do cache.
//
// Retorna ErrConfigNotFound quando a instância não tem config Meta ativa.
// Outros erros indicam falha de banco ou de decriptação — o caller deve
// tratar de forma diferente de ErrConfigNotFound.
func GetMetaConfig(instanceID string) (*MetaConfig, error) {
	if v, ok := cache.Load(instanceID); ok {
		if e := v.(cacheEntry); time.Since(e.fetchedAt) < cacheTTL {
			return e.config, nil
		}
	}

	cfg, err := fetchConfig(instanceID)
	if err != nil {
		return nil, err
	}

	cache.Store(instanceID, cacheEntry{config: cfg, fetchedAt: time.Now()})
	return cfg, nil
}

// GetInstanceIDByPhoneNumberID resolve um phone_number_id da Meta pro nome
// legível da instância Evolution correspondente.
//
// Resultado é cacheado por 5 minutos. Retorna ErrConfigNotFound quando não
// há config ativa pra esse phone_number_id.
func GetInstanceIDByPhoneNumberID(phoneNumberID string) (string, error) {
	if v, ok := phoneNumberCache.Load(phoneNumberID); ok {
		if e := v.(phoneNumberCacheEntry); time.Since(e.fetchedAt) < cacheTTL {
			return e.instanceID, nil
		}
	}

	instanceID, err := fetchInstanceIDByPhone(phoneNumberID)
	if err != nil {
		return "", err
	}

	phoneNumberCache.Store(phoneNumberID, phoneNumberCacheEntry{
		instanceID: instanceID,
		fetchedAt:  time.Now(),
	})
	return instanceID, nil
}

// LookupEventIDByPhone retorna o event_id da notificação mais recente enviada
// (status sent/delivered) pro número informado, na instância informada —
// usado pra correlacionar o callback de entrega da Meta com o evento
// original que disparou o envio.
//
// notifications.id já cumpre o papel de event_id (ver
// NotificationRepository.Create) — sem necessidade de uma tabela separada
// tipo wa_message_logs.
//
// Retorna ("", false, nil) quando nenhuma notification correspondente é
// encontrada — o caller deve despachar o callback message.received com
// event_id: null. Retorna erro não-nil só em falha de banco.
func LookupEventIDByPhone(instanceID, phone string) (string, bool, error) {
	eventID, found, err := lookupEventIDByPhone(instanceID, phone)
	if err != nil {
		return "", false, fmt.Errorf("metaconfig: lookup event_id: %w", err)
	}
	return eventID, found, nil
}
