package core

import (
	"os"
	"strings"
)

const (
	EnvPostgresURL    = "PB_POSTGRES_URL"
	EnvPostgresSchema = "PB_POSTGRES_SCHEMA"

	PostgresMetadataCollectionsTable  = "_pb_collections"
	PostgresMetadataTableSchemasTable = "_pb_table_schemas"
)

// PostgresConfig holds optional PostgreSQL connection settings loaded from env.
type PostgresConfig struct {
	URL           string
	DefaultSchema string
}

// LoadPostgresConfigFromEnv loads PostgreSQL settings from environment variables.
func LoadPostgresConfigFromEnv() PostgresConfig {
	schema := strings.TrimSpace(os.Getenv(EnvPostgresSchema))
	if schema == "" {
		schema = "public"
	}

	return PostgresConfig{
		URL:           strings.TrimSpace(os.Getenv(EnvPostgresURL)),
		DefaultSchema: schema,
	}
}

func (c PostgresConfig) Enabled() bool {
	return c.URL != ""
}
