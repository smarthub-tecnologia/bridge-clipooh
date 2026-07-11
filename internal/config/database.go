package config

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func NewDatabasePool(ctx context.Context) (*pgxpool.Pool, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL não definida")
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	zap.L().Info("connected to PostgreSQL",
		zap.String("host", poolConfig.ConnConfig.Host),
	)
	return pool, nil
}

func NewDirectusDatabasePool(ctx context.Context) (*pgxpool.Pool, error) {
	databaseURL := os.Getenv("DIRECTUS_DATABASE_URL")
	if databaseURL == "" {
		return nil, fmt.Errorf("DIRECTUS_DATABASE_URL não definida")
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse directus database config: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create directus connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping directus database: %w", err)
	}

	zap.L().Info("connected to Directus PostgreSQL",
		zap.String("host", poolConfig.ConnConfig.Host),
	)
	return pool, nil
}

// NewSupabaseDatabasePool cria um pool de conexão para o Supabase (PostgreSQL externo).
// Opcional — apenas inicializado quando SUPABASE_DATABASE_URL está presente.
// Retorna erro se a variável estiver ausente ou se a conexão falhar.
func NewSupabaseDatabasePool(ctx context.Context) (*pgxpool.Pool, error) {
	databaseURL := os.Getenv("SUPABASE_DATABASE_URL")
	if databaseURL == "" {
		return nil, fmt.Errorf("SUPABASE_DATABASE_URL não definida")
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse supabase database config: %w", err)
	}

	// Supabase pooler (porta 6543 PgBouncer ou 5432 direto) exige sslmode=require.
	// A string de conexão já deve conter o parâmetro — não sobrescrevemos aqui.
	poolConfig.MaxConns = 5 // conexões conservadoras para Supabase (shared infra)
	poolConfig.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create supabase connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping supabase database: %w", err)
	}

	zap.L().Info("connected to Supabase PostgreSQL",
		zap.String("host", poolConfig.ConnConfig.Host),
	)
	return pool, nil
}
