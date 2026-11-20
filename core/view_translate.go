package core

import (
	"errors"
	"strings"
	"unicode"
)

// translateSQLiteView rewrites the common SQLite-only constructs of a
// PocketBase view query for PostgreSQL. It is used only when
// importing PocketBase backups.
//
// It works on tokens, not a full SQL grammar: anything it does not
// recognize is left unchanged, and the caller is expected to try the
// result and fall back (skip the view) when the database rejects it.
//
// changed reports whether any rewrite was applied.
func translateSQLiteView(query string, d SQLDialect) (result string, changed bool, err error) {
	tokens := lexSQL(query)
	nodes, rest, err := groupSQL(tokens)
	if err != nil {
		return query, false, err
	}
	if len(rest) > 0 {
		return query, false, errors.New("unbalanced parentheses")
	}

	t := &viewTranslator{dialect: d}
	nodes = t.rewrite(nodes)

	result = renderSQL(nodes, d)
	return result, t.changed || result != query, nil
}

type sqlTokKind int

const (
	sqlSpace sqlTokKind = iota
	sqlComment
	sqlString  // '...'
	sqlQuoted  // "..." `...` [...]: an identifier; text is the unquoted name
	sqlWord    // keyword, identifier or function name
	sqlNumber  // numeric literal
	sqlPunct   // operator or separator
	sqlGroup   // (...) with children
	sqlRaw     // already translated text
	sqlOpenTok // internal: "("
	sqlShutTok // internal: ")"
)

type sqlNode struct {
	kind     sqlTokKind
	text     string
	children []*sqlNode
}

func lexSQL(s string) []*sqlNode {
	var out []*sqlNode
	r := []rune(s)
	for i := 0; i < len(r); {
		c := r[i]
		start := i
		switch {
		case unicode.IsSpace(c):
			for i < len(r) && unicode.IsSpace(r[i]) {
				i++
			}
			out = append(out, &sqlNode{kind: sqlSpace, text: string(r[start:i])})
		case c == '-' && i+1 < len(r) && r[i+1] == '-':
			for i < len(r) && r[i] != '\n' {
				i++
			}
			out = append(out, &sqlNode{kind: sqlComment, text: string(r[start:i])})
		case c == '/' && i+1 < len(r) && r[i+1] == '*':
			i += 2
			for i < len(r) && !(r[i] == '*' && i+1 < len(r) && r[i+1] == '/') {
				i++
			}
			i = min(i+2, len(r))
			out = append(out, &sqlNode{kind: sqlComment, text: string(r[start:i])})
		case c == '\'':
			i = scanQuoted(r, i, '\'')
			out = append(out, &sqlNode{kind: sqlString, text: string(r[start:i])})
		case c == '"' || c == '`':
			i = scanQuoted(r, i, c)
			out = append(out, &sqlNode{kind: sqlQuoted, text: unquoteSQL(string(r[start:i]), c)})
		case c == '[':
			for i < len(r) && r[i] != ']' {
				i++
			}
			i = min(i+1, len(r))
			out = append(out, &sqlNode{kind: sqlQuoted, text: strings.TrimSuffix(string(r[start+1:i]), "]")})
		case unicode.IsLetter(c) || c == '_':
			for i < len(r) && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i]) || r[i] == '_' || r[i] == '$') {
				i++
			}
			out = append(out, &sqlNode{kind: sqlWord, text: string(r[start:i])})
		case unicode.IsDigit(c):
			for i < len(r) && (unicode.IsDigit(r[i]) || r[i] == '.' || r[i] == 'e' || r[i] == 'E') {
				i++
			}
			out = append(out, &sqlNode{kind: sqlNumber, text: string(r[start:i])})
		case c == '(':
			i++
			out = append(out, &sqlNode{kind: sqlOpenTok, text: "("})
		case c == ')':
			i++
			out = append(out, &sqlNode{kind: sqlShutTok, text: ")"})
		default:
			i++
			if i < len(r) {
				switch string(r[start : i+1]) {
				case "||", "==", "!=", "<>", "<=", ">=", "::":
					i++
				}
			}
			out = append(out, &sqlNode{kind: sqlPunct, text: string(r[start:i])})
		}
	}
	return out
}

// scanQuoted returns the index after the closing quote, honoring doubled quotes.
func scanQuoted(r []rune, i int, q rune) int {
	for i++; i < len(r); i++ {
		if r[i] == q {
			if i+1 < len(r) && r[i+1] == q {
				i++
				continue
			}
			return i + 1
		}
	}
	return len(r)
}

func unquoteSQL(s string, q rune) string {
	s = strings.TrimPrefix(s, string(q))
	s = strings.TrimSuffix(s, string(q))
	return strings.ReplaceAll(s, string(q)+string(q), string(q))
}

// groupSQL nests parenthesized tokens into sqlGroup nodes.
func groupSQL(tokens []*sqlNode) (nodes []*sqlNode, rest []*sqlNode, err error) {
	for len(tokens) > 0 {
		tok := tokens[0]
		tokens = tokens[1:]
		switch tok.kind {
		case sqlOpenTok:
			children, remaining, err := groupSQL(tokens)
			if err != nil {
				return nil, nil, err
			}
			if len(remaining) == 0 || remaining[0].kind != sqlShutTok {
				return nil, nil, errors.New("unbalanced parentheses")
			}
			tokens = remaining[1:]
			nodes = append(nodes, &sqlNode{kind: sqlGroup, children: children})
		case sqlShutTok:
			return nodes, append([]*sqlNode{tok}, tokens...), nil
		default:
			nodes = append(nodes, tok)
		}
	}
	return nodes, nil, nil
}

func renderSQL(nodes []*sqlNode, d SQLDialect) string {
	var sb strings.Builder
	for _, n := range nodes {
		switch n.kind {
		case sqlGroup:
			sb.WriteString("(" + renderSQL(n.children, d) + ")")
		case sqlQuoted:
			sb.WriteString(d.Quote(n.text))
		default:
			sb.WriteString(n.text)
		}
	}
	return sb.String()
}

type viewTranslator struct {
	dialect SQLDialect
	changed bool
}

func (t *viewTranslator) raw(s string) *sqlNode {
	t.changed = true
	return &sqlNode{kind: sqlRaw, text: s}
}

func (t *viewTranslator) render(nodes []*sqlNode) string {
	return strings.TrimSpace(renderSQL(nodes, t.dialect))
}

// rewrite translates a node sequence, children first.
func (t *viewTranslator) rewrite(nodes []*sqlNode) []*sqlNode {
	for _, n := range nodes {
		if n.kind == sqlGroup {
			n.children = t.rewrite(n.children)
		}
	}

	// function calls: word [space] (args)
	out := make([]*sqlNode, 0, len(nodes))
	for i := 0; i < len(nodes); i++ {
		n := nodes[i]
		if n.kind == sqlWord && !isSQLKeyword(n.text) && !t.afterDot(out) {
			if j := nextNonSpace(nodes, i+1); j >= 0 && nodes[j].kind == sqlGroup {
				if replaced := t.function(strings.ToLower(n.text), nodes[j]); replaced != nil {
					out = append(out, replaced)
					i = j
					continue
				}
			}
		}
		out = append(out, t.keyword(n))
	}

	return out
}

func (t *viewTranslator) afterDot(out []*sqlNode) bool {
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].kind == sqlSpace {
			continue
		}
		return out[i].kind == sqlPunct && out[i].text == "."
	}
	return false
}

func (t *viewTranslator) keyword(n *sqlNode) *sqlNode {
	switch {
	case n.kind == sqlPunct && n.text == "==":
		return t.raw("=")
	case n.kind == sqlWord && strings.EqualFold(n.text, "like"):
		// SQLite LIKE ignores ASCII case.
		return t.raw("ILIKE")
	}
	return n
}

func splitArgs(group *sqlNode) [][]*sqlNode {
	if len(group.children) == 0 {
		return nil
	}
	args := [][]*sqlNode{{}}
	for _, c := range group.children {
		if c.kind == sqlPunct && c.text == "," {
			args = append(args, []*sqlNode{})
			continue
		}
		args[len(args)-1] = append(args[len(args)-1], c)
	}
	return args
}

func trimSpaceNodes(nodes []*sqlNode) []*sqlNode {
	for len(nodes) > 0 && (nodes[0].kind == sqlSpace || nodes[0].kind == sqlComment) {
		nodes = nodes[1:]
	}
	for len(nodes) > 0 && (nodes[len(nodes)-1].kind == sqlSpace || nodes[len(nodes)-1].kind == sqlComment) {
		nodes = nodes[:len(nodes)-1]
	}
	return nodes
}

// stringLiteral returns the value of an argument that is a single string literal.
func stringLiteral(arg []*sqlNode) (string, bool) {
	arg = trimSpaceNodes(arg)
	if len(arg) != 1 || arg[0].kind != sqlString {
		return "", false
	}
	return unquoteSQL(arg[0].text, '\''), true
}

func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// function returns the translation of name(args), or nil to keep it.
func (t *viewTranslator) function(name string, group *sqlNode) *sqlNode {
	args := splitArgs(group)
	r := make([]string, len(args))
	for i, a := range args {
		r[i] = t.render(a)
	}
	switch name {
	case "ifnull":
		if len(r) == 2 {
			return t.raw("COALESCE(" + r[0] + ", " + r[1] + ")")
		}
	case "iif":
		if len(r) == 3 {
			return t.raw("(CASE WHEN " + r[0] + " THEN " + r[1] + " ELSE " + r[2] + " END)")
		}
	case "total":
		if len(r) == 1 {
			return t.raw("COALESCE(SUM(" + r[0] + "), 0)")
		}
	case "instr":
		if len(r) == 2 {
			return t.raw("STRPOS(" + r[0] + ", " + r[1] + ")")
		}
	case "round":
		if len(r) == 2 {
			return t.raw("ROUND(CAST(" + r[0] + " AS NUMERIC), " + r[1] + ")")
		}
	case "cast":
		return t.cast(args)
	case "group_concat":
		return t.groupConcat(args)
	case "json_extract":
		if len(r) == 2 {
			return t.raw("(jsonb_path_query_first(CAST(" + r[0] + " AS jsonb), CAST(" + r[1] + " AS jsonpath)) #>> '{}')")
		}
	case "json_array_length":
		if len(r) == 1 {
			return t.raw("jsonb_array_length(CAST(" + r[0] + " AS jsonb))")
		}
	case "date", "time", "datetime", "strftime":
		return t.dateFunc(name, args)
	}
	return nil
}

func (t *viewTranslator) cast(args [][]*sqlNode) *sqlNode {
	if len(args) != 1 {
		return nil
	}
	arg := args[0]
	as := -1
	for i, n := range arg {
		if n.kind == sqlWord && strings.EqualFold(n.text, "as") {
			as = i
		}
	}
	if as < 0 {
		return nil
	}
	typ := strings.ToUpper(t.render(arg[as+1:]))
	expr := t.render(arg[:as])

	mapped := map[string]string{
		"INT": "BIGINT", "INTEGER": "BIGINT",
		"REAL": "DOUBLE PRECISION", "FLOAT": "DOUBLE PRECISION", "DOUBLE": "DOUBLE PRECISION",
		"BLOB": "BYTEA",
	}[typ]
	if mapped == "" {
		return nil
	}
	return t.raw("CAST(" + expr + " AS " + mapped + ")")
}

func (t *viewTranslator) groupConcat(args [][]*sqlNode) *sqlNode {
	if len(args) == 0 || len(args) > 2 {
		return nil
	}
	first := trimSpaceNodes(args[0])
	distinct := ""
	if len(first) > 0 && first[0].kind == sqlWord && strings.EqualFold(first[0].text, "distinct") {
		distinct = "DISTINCT "
		first = first[1:]
	}
	expr := t.render(first)
	sep := "','"
	if len(args) == 2 {
		sep = t.render(args[1])
	}
	return t.raw("STRING_AGG(" + distinct + "CAST(" + expr + " AS TEXT), " + sep + ")")
}

// dateFunc translates date/time/datetime/strftime without modifiers.
func (t *viewTranslator) dateFunc(name string, args [][]*sqlNode) *sqlNode {
	var format string
	switch name {
	case "date":
		format = "%Y-%m-%d"
	case "time":
		format = "%H:%M:%S"
	case "datetime":
		format = "%Y-%m-%d %H:%M:%S"
	case "strftime":
		if len(args) == 0 {
			return nil
		}
		f, ok := stringLiteral(args[0])
		if !ok {
			return nil
		}
		format, args = f, args[1:]
	}
	if len(args) > 1 {
		return nil // modifiers such as '+1 day' are not translated
	}

	isNow := len(args) == 0
	if !isNow {
		if v, ok := stringLiteral(args[0]); ok && strings.EqualFold(v, "now") {
			isNow = true
		}
	}

	pattern, ok := postgresDatePattern(format)
	if !ok {
		return nil
	}
	ts := "(now() AT TIME ZONE 'UTC')"
	if !isNow {
		ts = "(CAST(NULLIF(" + t.render(args[0]) + ", '') AS timestamptz) AT TIME ZONE 'UTC')"
	}
	return t.raw("to_char(" + ts + ", " + sqlQuote(pattern) + ")")
}

func postgresDatePattern(format string) (string, bool) {
	codes := map[rune]string{'Y': "YYYY", 'm': "MM", 'd': "DD", 'H': "HH24", 'M': "MI", 'S': "SS", 'j': "DDD", '%': "%"}
	var sb strings.Builder
	r := []rune(format)
	for i := 0; i < len(r); i++ {
		if r[i] == '%' {
			if i+1 >= len(r) {
				return "", false
			}
			code, ok := codes[r[i+1]]
			if !ok {
				return "", false
			}
			sb.WriteString(code)
			i++
			continue
		}
		if unicode.IsLetter(r[i]) {
			sb.WriteString(`"` + string(r[i]) + `"`) // literal text, not a pattern
			continue
		}
		sb.WriteRune(r[i])
	}
	return sb.String(), true
}

func nextNonSpace(nodes []*sqlNode, i int) int {
	for ; i >= 0 && i < len(nodes); i++ {
		if nodes[i].kind != sqlSpace && nodes[i].kind != sqlComment {
			return i
		}
	}
	return -1
}

var sqlKeywords = map[string]bool{
	"select": true, "from": true, "where": true, "and": true, "or": true, "not": true,
	"in": true, "is": true, "null": true, "like": true, "glob": true, "as": true, "on": true,
	"join": true, "left": true, "right": true, "inner": true, "outer": true, "cross": true,
	"group": true, "by": true, "order": true, "having": true, "limit": true, "offset": true,
	"union": true, "all": true, "distinct": true, "case": true, "when": true, "then": true,
	"else": true, "end": true, "exists": true, "between": true, "asc": true, "desc": true,
	"with": true, "over": true, "partition": true, "collate": true, "escape": true,
	"true": true, "false": true,
}

func isSQLKeyword(word string) bool {
	return sqlKeywords[strings.ToLower(word)]
}
