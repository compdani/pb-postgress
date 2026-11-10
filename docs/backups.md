# Backups

Backups are on demand (`POST /api/backups`); the backup cron is removed, so regular protection is the database server's job (base backups, PITR, binlogs).

- A backup is a portable zip that restores on PostgreSQL and MySQL alike, and keeps token and OAuth2 secrets, so users stay signed in after a restore (restoring encrypted settings needs the same `PB_ENCRYPTION` key).
- `POST /api/backups/{key}/restore` also accepts a standard PocketBase backup (`data.db` + `storage/`) from **v0.23.0 to v0.41.x**. View queries are translated from SQLite where possible; a view that still fails is skipped and listed afterwards. `auxiliary.db` (logs) is not imported.

Details: [DATABASE_MIGRATION.md](../DATABASE_MIGRATION.md#backups).

