package metaconfig

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// querier é o subconjunto de *pgxpool.Pool usado por este pacote — permite
// injetar um fake em teste sem subir um Postgres real.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// pool é injetado uma vez no startup via SetPool.
var pool querier

// SetPool injeta o pool de conexão do Postgres do próprio bridge no pacote
// metaconfig. Deve ser chamado uma única vez durante a inicialização do
// servidor, antes de começar a servir requisições.
func SetPool(p *pgxpool.Pool) {
	pool = p
}

var errPoolNotConfigured = errors.New("metaconfig: pool não configurado — chame SetPool no startup")

// fetchConfig consulta meta_provider_configs pelo nome legível da instância.
// Retorna ErrConfigNotFound quando não há config ativa.
func fetchConfig(instanceID string) (*MetaConfig, error) {
	if pool == nil {
		return nil, errPoolNotConfigured
	}

	var phoneNumberID, accessTokenEnc, tokenKeyVersion string
	err := pool.QueryRow(
		context.Background(),
		`SELECT wa_phone_number_id, wa_access_token_enc, token_key_version
		 FROM meta_provider_configs
		 WHERE evolution_instance_id = $1 AND is_active = true
		 LIMIT 1`,
		instanceID,
	).Scan(&phoneNumberID, &accessTokenEnc, &tokenKeyVersion)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("metaconfig: query meta_provider_configs: %w", err)
	}

	token, err := decryptFn(accessTokenEnc, tokenKeyVersion)
	if err != nil {
		return nil, fmt.Errorf("metaconfig: decrypt token: %w", err)
	}

	return &MetaConfig{PhoneNumberID: phoneNumberID, AccessToken: token}, nil
}

// fetchInstanceIDByPhone resolve um phone_number_id da Meta pro nome legível
// da instância Evolution correspondente.
func fetchInstanceIDByPhone(phoneNumberID string) (string, error) {
	if pool == nil {
		return "", errPoolNotConfigured
	}

	var instanceID string
	err := pool.QueryRow(
		context.Background(),
		`SELECT evolution_instance_id
		 FROM meta_provider_configs
		 WHERE wa_phone_number_id = $1 AND is_active = true
		 LIMIT 1`,
		phoneNumberID,
	).Scan(&instanceID)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrConfigNotFound
	}
	if err != nil {
		return "", fmt.Errorf("metaconfig: query phone lookup: %w", err)
	}

	return instanceID, nil
}

// lookupEventIDByPhone busca o id (== event_id) da notification mais recente
// enviada/entregue pro telefone informado, na instância informada.
func lookupEventIDByPhone(instanceID, phone string) (string, bool, error) {
	if pool == nil {
		return "", false, errPoolNotConfigured
	}

	var eventID string
	err := pool.QueryRow(
		context.Background(),
		`SELECT id::text FROM notifications
		 WHERE instance_name = $1 AND to_number = $2 AND status IN ('sent', 'delivered')
		 ORDER BY sent_at DESC LIMIT 1`,
		instanceID, phone,
	).Scan(&eventID)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("metaconfig: query notifications: %w", err)
	}

	return eventID, true, nil
}
