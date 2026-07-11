package metaconfig

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// supabasePool é injetado uma vez no startup via SetSupabasePool.
// Quando nil, GetMetaConfig e GetInstanceIDByPhoneNumberID usam Directus REST como fallback.
var supabasePool *pgxpool.Pool

// SetSupabasePool injeta o pool de conexão do Supabase no pacote metaconfig.
// Deve ser chamado uma única vez durante a inicialização do servidor,
// antes de começar a servir requisições.
func SetSupabasePool(pool *pgxpool.Pool) {
	supabasePool = pool
}

// fetchFromSupabase consulta meta_provider_configs diretamente no Supabase via SQL.
// Retorna ErrConfigNotFound quando evolution_instance_id não tem config ativa.
// Outros erros (rede, decriptação) são propagados como-estão.
func fetchFromSupabase(instanceID string) (*MetaConfig, error) {
	var phoneNumberID, accessTokenEnc, tokenKeyVersion string
	err := supabasePool.QueryRow(
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
		return nil, fmt.Errorf("metaconfig: supabase query: %w", err)
	}

	token, err := decryptFn(accessTokenEnc, tokenKeyVersion)
	if err != nil {
		return nil, fmt.Errorf("metaconfig: decrypt token: %w", err)
	}

	return &MetaConfig{PhoneNumberID: phoneNumberID, AccessToken: token}, nil
}

// fetchInstanceIDByPhoneFromSupabase resolve um Meta phone_number_id para o
// evolution_instance_id correspondente, usando query SQL direta no Supabase.
// Retorna ErrConfigNotFound quando não há config ativa para o phone_number_id.
func fetchInstanceIDByPhoneFromSupabase(phoneNumberID string) (string, error) {
	var instanceID string
	err := supabasePool.QueryRow(
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
		return "", fmt.Errorf("metaconfig: supabase phone query: %w", err)
	}

	return instanceID, nil
}

// resolveInstanceUUIDFromSupabase resolve o nome legível da instância (ex.:
// "wa-xxx") pro id interno (UUID) de evolution_instances, via SQL direto.
func resolveInstanceUUIDFromSupabase(instanceID string) (string, bool, error) {
	var id string
	err := supabasePool.QueryRow(
		context.Background(),
		`SELECT id FROM evolution_instances WHERE evolution_instance_id = $1 LIMIT 1`,
		instanceID,
	).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("metaconfig: supabase resolve instance uuid: %w", err)
	}
	return id, true, nil
}

// lookupEventIDByPhoneFromSupabase é o equivalente SQL direto de
// LookupEventIDByPhone — resolvedInstanceID já deve ser o UUID interno (ver
// resolveInstanceUUID), não o nome legível.
func lookupEventIDByPhoneFromSupabase(resolvedInstanceID, phone string) (string, bool, error) {
	var eventID string
	err := supabasePool.QueryRow(
		context.Background(),
		`SELECT event_id FROM wa_message_logs
		 WHERE evolution_instance_id = $1 AND recipient_phone = $2 AND status IN ('sent', 'delivered')
		 ORDER BY sent_at DESC LIMIT 1`,
		resolvedInstanceID, phone,
	).Scan(&eventID)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("metaconfig: supabase lookup event_id: %w", err)
	}
	return eventID, true, nil
}
