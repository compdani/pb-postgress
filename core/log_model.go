package core

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pocketbase/pocketbase/tools/types"
)

var (
	_ Model      = (*Log)(nil)
	_ DBExporter = (*Log)(nil)
)

const LogsTableName = "_logs"

const (
	defaultMaxLogDataSize    = 16 << 10 // ~16kb
	defaultMaxLogMessageSize = 8000     // characters
)

type Log struct {
	BaseModel

	Created types.DateTime     `db:"created" json:"created"`
	Data    types.JSONMap[any] `db:"data" json:"data"`
	Message string             `db:"message" json:"message"`
	Level   int                `db:"level" json:"level"`
}

func (m *Log) TableName() string {
	return LogsTableName
}

// DBExport prepares and exports the current log model for db persistence.
//
// It also truncates the log's message and data to ensure that it is
// under app.Settings().Logs.MaxDataSize.
func (m *Log) DBExport(app App) (map[string]any, error) {
	result := map[string]any{
		"id":      m.Id,
		"created": m.Created,
		"level":   m.Level,
	}

	// PostgreSQL text columns reject invalid UTF-8 and NUL bytes (SQLite
	// accepts both). Request log messages contain the path-unescaped URL,
	// so a probe like "/?%ADd" or "/%00" would fail the insert and abort
	// the whole batch transaction.
	message := strings.ToValidUTF8(m.Message, "\uFFFD")
	message = strings.ReplaceAll(message, "\x00", "\uFFFD")

	// truncate the message (by characters, to not split multi-byte runes)
	if utf8.RuneCountInString(message) > defaultMaxLogMessageSize {
		message = string([]rune(message)[:defaultMaxLogMessageSize])
	}
	result["message"] = message

	if len(m.Data) == 0 {
		result["data"] = m.Data
		return result, nil
	}

	maxDataSize := app.Settings().Logs.MaxDataSize
	if maxDataSize == 0 {
		maxDataSize = defaultMaxLogDataSize
	}

	rawData, err := m.Data.MarshalJSON()
	if err != nil {
		return nil, err
	}

	if int64(len(rawData)) > maxDataSize {
		rawData, err = truncateLogData(m.Data, maxDataSize)
		if err != nil {
			return nil, err
		}
	}

	result["data"] = types.JSONRaw(replaceJSONNul(rawData))

	return result, nil
}

// replaceJSONNul replaces the \u0000 escapes of the encoded json with
// \ufffd, since a jsonb column rejects them. The escapes are scanned in
// pairs, so an escaped backslash followed by "u0000" is left unchanged.
func replaceJSONNul(raw []byte) []byte {
	if !bytes.Contains(raw, []byte(`\u0000`)) {
		return raw
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 >= len(raw) {
			out = append(out, raw[i])
			continue
		}
		if bytes.HasPrefix(raw[i:], []byte(`\u0000`)) {
			out = append(out, `\ufffd`...)
			i += 5
			continue
		}
		out = append(out, raw[i], raw[i+1])
		i++
	}
	return out
}

// truncateLogData keeps as many whole top-level entries (in key order)
// as fit in maxSize bytes and marks the result with "__pb_truncated__":true,
// so that the stored data is always valid json.
func truncateLogData(data map[string]any, maxSize int64) ([]byte, error) {
	const marker = `"__pb_truncated__":true`

	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	kept := map[string]any{}
	size := int64(len(marker) + 2) // braces + marker

	for _, k := range keys {
		rawKey, err := json.Marshal(k)
		if err != nil {
			continue
		}
		rawValue, err := json.Marshal(data[k])
		if err != nil {
			continue
		}

		entrySize := int64(len(rawKey)+len(rawValue)) + 2 // colon and comma
		if size+entrySize > maxSize {
			continue
		}

		size += entrySize
		kept[k] = data[k]
	}

	kept["__pb_truncated__"] = true

	return json.Marshal(kept)
}
