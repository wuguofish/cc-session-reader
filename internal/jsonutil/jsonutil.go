// Package jsonutil provides shared helpers for untyped JSON map traversal.
package jsonutil

import (
	"bytes"
	"encoding/json"
	"strings"
)

func GetStr(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func GetNum(m map[string]interface{}, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func GetMap(m map[string]interface{}, key string) map[string]interface{} {
	if v, ok := m[key].(map[string]interface{}); ok {
		return v
	}
	return nil
}

func GetInputMap(tb map[string]interface{}) map[string]interface{} {
	if inp, ok := tb["input"].(map[string]interface{}); ok {
		return inp
	}
	return map[string]interface{}{}
}

func MarshalNoEscape(v interface{}) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "{}"
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
