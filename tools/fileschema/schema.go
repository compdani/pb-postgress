// Package fileschema defines the shared core and bucket database tables.
package fileschema

import "fmt"

func Hosts(quoted string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id VARCHAR(128) PRIMARY KEY,
		url TEXT NOT NULL,
		mode VARCHAR(32) NOT NULL,
		secret VARCHAR(512) NOT NULL DEFAULT '',
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`, quoted)
}

func Changes(quoted string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id BIGSERIAL PRIMARY KEY,
		op VARCHAR(16) NOT NULL,
		collection TEXT NOT NULL,
		record_id TEXT NOT NULL,
		filename TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`, quoted)
}
