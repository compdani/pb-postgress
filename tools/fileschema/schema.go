// Package fileschema defines the shared core and bucket database tables.
package fileschema

import "fmt"

func Hosts(driver, quoted string) string {
	if driver == "mysql" {
		return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id VARCHAR(128) NOT NULL PRIMARY KEY,
			url TEXT NOT NULL,
			mode VARCHAR(32) NOT NULL,
			secret VARCHAR(512) NOT NULL DEFAULT '',
			registered_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
		)`, quoted)
	}
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id VARCHAR(128) PRIMARY KEY,
		url TEXT NOT NULL,
		mode VARCHAR(32) NOT NULL,
		secret VARCHAR(512) NOT NULL DEFAULT '',
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`, quoted)
}

func Changes(driver, quoted string) string {
	if driver == "mysql" {
		return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			op VARCHAR(16) NOT NULL,
			collection VARCHAR(255) NOT NULL,
			record_id VARCHAR(255) NOT NULL,
			filename VARCHAR(1024) NOT NULL,
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
		)`, quoted)
	}
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id BIGSERIAL PRIMARY KEY,
		op VARCHAR(16) NOT NULL,
		collection TEXT NOT NULL,
		record_id TEXT NOT NULL,
		filename TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`, quoted)
}
