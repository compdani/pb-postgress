package core

import "testing"

func TestTranslateSQLiteView(t *testing.T) {
	t.Parallel()

	scenarios := []struct {
		name  string
		query string
		pg    string
		mysql string
	}{
		{
			"unchanged",
			"select id, title from posts",
			"select id, title from posts",
			"select id, title from posts",
		},
		{
			"quoted identifiers",
			`select "id", [title], ` + "`body`" + ` from "posts"`,
			`select "id", "title", "body" from "posts"`,
			"select `id`, `title`, `body` from `posts`",
		},
		{
			"ifnull, iif and total",
			"select ifnull(a, 'x'), iif(b > 1, 'big', 'small'), total(n) from t",
			"select COALESCE(a, 'x'), (CASE WHEN b > 1 THEN 'big' ELSE 'small' END), COALESCE(SUM(n), 0) from t",
			"select ifnull(a, 'x'), (CASE WHEN b > 1 THEN 'big' ELSE 'small' END), COALESCE(SUM(n), 0) from t",
		},
		{
			"concatenation",
			"select t.a || '-' || upper(b) || (c) as label from t",
			"select t.a || '-' || upper(b) || (c) as label from t",
			"select CONCAT(t.a, '-', upper(b), (c)) as label from t",
		},
		{
			"casts",
			"select cast(n as int), CAST(x AS real), cast(y as text) from t",
			"select CAST(n AS BIGINT), CAST(x AS DOUBLE PRECISION), cast(y as text) from t",
			"select CAST(n AS SIGNED), CAST(x AS DOUBLE), CAST(y AS CHAR) from t",
		},
		{
			"group_concat",
			"select group_concat(name), group_concat(distinct tag), group_concat(name, '; ') from t",
			"select STRING_AGG(CAST(name AS TEXT), ','), STRING_AGG(DISTINCT CAST(tag AS TEXT), ','), STRING_AGG(CAST(name AS TEXT), '; ') from t",
			"select group_concat(name), group_concat(distinct tag), GROUP_CONCAT(name SEPARATOR '; ') from t",
		},
		{
			"json",
			"select json_extract(data, '$.a.b'), json_array_length(tags) from t",
			"select (jsonb_path_query_first(CAST(data AS jsonb), CAST('$.a.b' AS jsonpath)) #>> '{}'), jsonb_array_length(CAST(tags AS jsonb)) from t",
			"select JSON_UNQUOTE(JSON_EXTRACT(data, '$.a.b')), JSON_LENGTH(tags) from t",
		},
		{
			"dates",
			"select strftime('%Y-%m', created), date('now'), datetime(updated) from t",
			`select to_char((CAST(NULLIF(created, '') AS timestamptz) AT TIME ZONE 'UTC'), 'YYYY-MM'), to_char((now() AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), to_char((CAST(NULLIF(updated, '') AS timestamptz) AT TIME ZONE 'UTC'), 'YYYY-MM-DD HH24:MI:SS') from t`,
			"select DATE_FORMAT(CAST(REPLACE(REPLACE(created, 'T', ' '), 'Z', '') AS DATETIME(3)), '%Y-%m'), DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%d'), DATE_FORMAT(CAST(REPLACE(REPLACE(updated, 'T', ' '), 'Z', '') AS DATETIME(3)), '%Y-%m-%d %H:%i:%s') from t",
		},
		{
			"untranslatable date modifiers and formats are kept",
			"select date(created, '+1 day'), strftime('%w', created) from t",
			"select date(created, '+1 day'), strftime('%w', created) from t",
			"select date(created, '+1 day'), strftime('%w', created) from t",
		},
		{
			"like, == and instr/length",
			"select * from t where name like 'a%' and n == 1 and instr(name, 'x') > 0 and length(name) > 2",
			"select * from t where name ILIKE 'a%' and n = 1 and STRPOS(name, 'x') > 0 and length(name) > 2",
			"select * from t where LOWER(name) LIKE LOWER('a%') and n = 1 and instr(name, 'x') > 0 and CHAR_LENGTH(name) > 2",
		},
		{
			"not like",
			"select * from t where t.name not like 'a%'",
			"select * from t where t.name not ILIKE 'a%'",
			"select * from t where LOWER(t.name) NOT LIKE LOWER('a%')",
		},
		{
			"nested functions and strings with keywords",
			"select ifnull(cast(n as integer), 0) as 'select || from' from t -- ifnull(x)",
			"select COALESCE(CAST(n AS BIGINT), 0) as 'select || from' from t -- ifnull(x)",
			"select ifnull(CAST(n AS SIGNED), 0) as 'select || from' from t -- ifnull(x)",
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			for _, c := range []struct {
				d        SQLDialect
				expected string
			}{{DialectPostgres, s.pg}, {DialectMySQL, s.mysql}} {
				result, changed, err := translateSQLiteView(s.query, c.d)
				if err != nil {
					t.Fatal(err)
				}
				if result != c.expected {
					t.Errorf("dialect %d:\nexpected %s\ngot      %s", c.d, c.expected, result)
				}
				if result != s.query && !changed {
					t.Errorf("dialect %d: expected changed to be true", c.d)
				}
			}
		})
	}
}

func TestTranslateSQLiteViewUnbalanced(t *testing.T) {
	t.Parallel()

	query := "select (a from t"
	result, changed, err := translateSQLiteView(query, DialectPostgres)
	if err == nil || changed || result != query {
		t.Fatalf("expected the query back unchanged with an error, got %q %v %v", result, changed, err)
	}
}
