package core

import (
	"encoding/json"
	"sort"

	"github.com/pocketbase/pocketbase/tools/types"
)

var (
	_ Model      = (*Log)(nil)
	_ DBExporter = (*Log)(nil)
)

const LogsTableName = "_logs"

const (
	defaultMaxLogDataSize    = 16 << 10 // ~16kb
	defaultMaxLogMessageSize = 8000
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

	// truncate the raw message bytes
	if len(m.Message) > defaultMaxLogMessageSize {
		result["message"] = m.Message[:defaultMaxLogMessageSize]
	} else {
		result["message"] = m.Message
	}

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

	result["data"] = types.JSONRaw(rawData)

	return result, nil
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
