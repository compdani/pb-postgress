package dbutils

// JSONEach and JSONArrayLength use the PostgreSQL dialect.
func JSONEach(column string) string          { return JSONEachPostgres(column) }
func JSONArrayLength(column string) string   { return JSONArrayLengthPostgres(column) }
func JSONExtract(column, path string) string { return JSONExtractPostgres(column, path) }
