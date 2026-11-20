package migrations

import (
	"github.com/pocketbase/pocketbase/core"
)

// _logs.message was VARCHAR(255) while MySQL was supported. It becomes TEXT
// again (as in upstream PocketBase), and its index is dropped: a btree entry
// is limited to ~2.7kb, and the logs filters search the message with ILIKE
// '%...%', which never uses it.
func init() {
	core.SystemMigrations.Register(func(txApp core.App) error {
		return core.ExecuteSchema(txApp, `
			DROP INDEX IF EXISTS idx_logs_message;
			ALTER TABLE {{_logs}} ALTER COLUMN [[message]] TYPE TEXT;
		`)
	}, nil)
}
