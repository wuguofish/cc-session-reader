package parser

import (
	"fmt"
	"strings"

	"claude-code-session-reader/internal/jsonutil"
)

// FormatTimestamp converts an ISO timestamp string to "MM-DD HH:MM" format.
func FormatTimestamp(tsStr string) string {
	if tsStr == "" {
		return "??-?? ??:??"
	}
	tsStr = strings.Replace(tsStr, "Z", "+00:00", 1)
	t, err := parseISO(tsStr)
	if err != nil {
		return "??-?? ??:??"
	}
	return t.Format("01-02 15:04")
}

// ExtractText extracts text from a message content field.
// Content can be a string or a list of content blocks.
func ExtractText(content interface{}) string {
	if s, ok := content.(string); ok {
		return s
	}
	if arr, ok := content.([]interface{}); ok {
		var parts []string
		for _, item := range arr {
			block, isMap := item.(map[string]interface{})
			if !isMap {
				continue
			}
			if jsonutil.GetStr(block, "type") == "text" {
				parts = append(parts, jsonutil.GetStr(block, "text"))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// GetToolUses extracts tool_use blocks from a content array.
func GetToolUses(content interface{}) []map[string]interface{} {
	arr, ok := content.([]interface{})
	if !ok {
		return nil
	}
	var results []map[string]interface{}
	for _, item := range arr {
		block, isMap := item.(map[string]interface{})
		if !isMap {
			continue
		}
		if jsonutil.GetStr(block, "type") == "tool_use" {
			results = append(results, block)
		}
	}
	return results
}

// ExtractToolResultText extracts text content and tool_use_id from a tool_result entry.
func ExtractToolResultText(entry map[string]interface{}) (string, string) {
	message := jsonutil.GetMap(entry, "message")
	if message == nil {
		return "", ""
	}
	content, ok := message["content"].([]interface{})
	if !ok {
		return "", ""
	}
	for _, item := range content {
		block, isMap := item.(map[string]interface{})
		if !isMap || jsonutil.GetStr(block, "type") != "tool_result" {
			continue
		}
		toolUseID := jsonutil.GetStr(block, "tool_use_id")
		sub := block["content"]
		if s, ok := sub.(string); ok {
			return s, toolUseID
		}
		if subArr, ok := sub.([]interface{}); ok {
			var parts []string
			for _, subItem := range subArr {
				subBlock, isMap := subItem.(map[string]interface{})
				if !isMap || jsonutil.GetStr(subBlock, "type") != "text" {
					continue
				}
				parts = append(parts, jsonutil.GetStr(subBlock, "text"))
			}
			return strings.Join(parts, "\n"), toolUseID
		}
	}
	return "", ""
}

// ExtractAllText extracts all text content from an entry, including tool inputs and results.
func ExtractAllText(entry map[string]interface{}) string {
	var parts []string
	message := jsonutil.GetMap(entry, "message")
	if message == nil {
		return ""
	}
	content := message["content"]
	if s, ok := content.(string); ok {
		parts = append(parts, s)
	} else if arr, ok := content.([]interface{}); ok {
		for _, item := range arr {
			block, isMap := item.(map[string]interface{})
			if !isMap {
				continue
			}
			switch jsonutil.GetStr(block, "type") {
			case "text":
				parts = append(parts, jsonutil.GetStr(block, "text"))
			case "tool_use":
				if inp := block["input"]; inp != nil {
					parts = append(parts, jsonutil.MarshalNoEscape(inp))
				}
			case "tool_result":
				sub := block["content"]
				if s, ok := sub.(string); ok {
					parts = append(parts, s)
				} else if subArr, ok := sub.([]interface{}); ok {
					for _, subItem := range subArr {
						subBlock, isSubMap := subItem.(map[string]interface{})
						if !isSubMap || jsonutil.GetStr(subBlock, "type") != "text" {
							continue
						}
						parts = append(parts, jsonutil.GetStr(subBlock, "text"))
					}
				}
			}
		}
	}

	tr := jsonutil.GetMap(entry, "toolUseResult")
	if tr != nil {
		for _, key := range []string{"stdout", "stderr", "output"} {
			if v, ok := tr[key]; ok && v != nil {
				parts = append(parts, fmt.Sprintf("%v", v))
			}
		}
	}

	return strings.Join(parts, "\n")
}

// IsNoise returns true if the entry is a noise type that should be skipped.
func IsNoise(entry map[string]interface{}) bool {
	entryType := jsonutil.GetStr(entry, "type")
	return NoiseTypes[entryType] || entryType == "system"
}
