package codex

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// PostgreSQL text and jsonb cannot represent U+0000. Replace it only in the
// displayed history projection; native commands, results and model input stay
// untouched. UseNumber keeps large integers and decimal values exact.
func projectedText(text string) string {
	return strings.ReplaceAll(text, "\x00", "\ufffd")
}

func projectedValue(value any) any {
	switch v := value.(type) {
	case string:
		return projectedText(v)
	case []any:
		for i, item := range v {
			v[i] = projectedValue(item)
		}
	case map[string]any:
		result := make(map[string]any, len(v))
		for _, key := range slices.Sorted(maps.Keys(v)) {
			name := projectedText(key)
			if _, exists := result[name]; exists {
				// Keep both values visible if replacement merges object keys.
				encoded, _ := json.Marshal(v)
				return string(encoded)
			}
			result[name] = projectedValue(v[key])
		}
		return result
	}
	return value
}

func projectedJSON(raw json.RawMessage) json.RawMessage {
	if !bytes.Contains(raw, []byte(`\u0000`)) {
		return raw
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return raw
	}
	result, err := json.Marshal(projectedValue(value))
	if err != nil {
		return raw
	}
	return result
}
