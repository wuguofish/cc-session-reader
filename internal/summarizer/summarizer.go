// Package summarizer provides tool call one-line summaries and user answer detection.
package summarizer

import (
	"fmt"
	"strings"

	"claude-code-session-reader/internal/jsonutil"
)

// UserAnswerPrefixes are the known prefixes for user answer tool results.
var UserAnswerPrefixes = []string{
	"User has answered your questions:",
	"Your questions have been answered:",
}

const (
	maxCommandLen  = 80
	maxSkillLen    = 80
	maxQuestionLen = 90
)

// SummarizeToolUse produces a one-line summary of a tool_use block.
func SummarizeToolUse(name string, inp map[string]interface{}) string {
	switch name {
	case "Bash":
		desc := jsonutil.GetStr(inp, "description")
		if desc != "" {
			return fmt.Sprintf("[Bash] %s", desc)
		}
		cmd := jsonutil.GetStr(inp, "command")
		return fmt.Sprintf("[Bash] %s", truncate(cmd, maxCommandLen))

	case "Read":
		path := jsonutil.GetStr(inp, "file_path")
		if path == "" {
			path = "?"
		}
		parts := strings.Split(path, "/")
		var short string
		if len(parts) >= 2 {
			short = strings.Join(parts[len(parts)-2:], "/")
		} else {
			short = path
		}
		return fmt.Sprintf("[Read] %s", short)

	case "Edit":
		path := jsonutil.GetStr(inp, "file_path")
		if path == "" {
			path = "?"
		}
		idx := strings.LastIndex(path, "/")
		filename := path
		if idx >= 0 {
			filename = path[idx+1:]
		}
		return fmt.Sprintf("[Edit] %s", filename)

	case "Write":
		path := jsonutil.GetStr(inp, "file_path")
		if path == "" {
			path = "?"
		}
		idx := strings.LastIndex(path, "/")
		filename := path
		if idx >= 0 {
			filename = path[idx+1:]
		}
		return fmt.Sprintf("[Write] %s", filename)

	case "Agent":
		desc := jsonutil.GetStr(inp, "description")
		if desc == "" {
			desc = "?"
		}
		sub := jsonutil.GetStr(inp, "subagent_type")
		if sub != "" {
			return fmt.Sprintf("[Agent(%s)] %s", sub, desc)
		}
		return fmt.Sprintf("[Agent] %s", desc)

	case "Grep":
		pat := jsonutil.GetStr(inp, "pattern")
		if pat == "" {
			pat = "?"
		}
		path := jsonutil.GetStr(inp, "path")
		if path != "" {
			return fmt.Sprintf("[Grep] \"%s\" in %s", pat, path)
		}
		return fmt.Sprintf("[Grep] \"%s\"", pat)

	case "Glob":
		pat := jsonutil.GetStr(inp, "pattern")
		if pat == "" {
			pat = "?"
		}
		return fmt.Sprintf("[Glob] %s", pat)

	case "Skill":
		skill := jsonutil.GetStr(inp, "skill")
		if skill == "" {
			skill = "?"
		}
		args := jsonutil.GetStr(inp, "args")
		result := fmt.Sprintf("[Skill] /%s %s", skill, args)
		return truncate(strings.TrimSpace(result), maxSkillLen)

	case "AskUserQuestion":
		qs, hasQuestions := inp["questions"]
		if !hasQuestions {
			return "[AskUserQuestion]"
		}
		qsList, isList := qs.([]interface{})
		if !isList || len(qsList) == 0 {
			return "[AskUserQuestion]"
		}
		var lines []string
		for i, q := range qsList {
			qMap, isMap := q.(map[string]interface{})
			if !isMap {
				continue
			}
			questionText := jsonutil.GetStr(qMap, "question")
			if questionText == "" {
				questionText = "?"
			}
			line := fmt.Sprintf("[AskUserQuestion] Q%d: %s", i+1, questionText)
			lines = append(lines, truncate(line, maxQuestionLen))
		}
		if len(lines) == 0 {
			return "[AskUserQuestion]"
		}
		return strings.Join(lines, "\n  ")

	case "ToolSearch":
		query := jsonutil.GetStr(inp, "query")
		if query == "" {
			query = "?"
		}
		return fmt.Sprintf("[ToolSearch] %s", query)

	default:
		return fmt.Sprintf("[%s]", name)
	}
}

// SummarizeToolResult produces a short status string from a toolUseResult entry.
func SummarizeToolResult(entry map[string]interface{}) string {
	tr, ok := entry["toolUseResult"].(map[string]interface{})
	if !ok {
		return ""
	}
	isSuccess := true
	if v, exists := tr["success"]; exists {
		if b, ok := v.(bool); ok {
			isSuccess = b
		}
	}

	status := "ok"
	if !isSuccess {
		status = "FAILED"
	}

	firstLine := extractFirstLineFromToolResult(entry)
	if firstLine != "" {
		return fmt.Sprintf(" -> %s: %s", status, firstLine)
	}
	return fmt.Sprintf(" -> %s", status)
}

// IsUserAnswer checks if a tool result entry contains a user answer.
func IsUserAnswer(entry map[string]interface{}) bool {
	message, ok := entry["message"].(map[string]interface{})
	if !ok {
		return false
	}
	content, ok := message["content"].([]interface{})
	if !ok {
		return false
	}
	for _, item := range content {
		block, isMap := item.(map[string]interface{})
		if !isMap {
			continue
		}
		if jsonutil.GetStr(block, "type") != "tool_result" {
			continue
		}
		sub, isStr := block["content"].(string)
		if !isStr {
			continue
		}
		for _, prefix := range UserAnswerPrefixes {
			if strings.HasPrefix(sub, prefix) {
				return true
			}
		}
	}
	return false
}

// ExtractUserAnswers extracts the user answer text from a tool result entry.
func ExtractUserAnswers(entry map[string]interface{}) string {
	message, ok := entry["message"].(map[string]interface{})
	if !ok {
		return ""
	}
	content, ok := message["content"].([]interface{})
	if !ok {
		return ""
	}
	for _, item := range content {
		block, isMap := item.(map[string]interface{})
		if !isMap {
			continue
		}
		if jsonutil.GetStr(block, "type") != "tool_result" {
			continue
		}
		sub, isStr := block["content"].(string)
		if !isStr {
			continue
		}
		for _, prefix := range UserAnswerPrefixes {
			if strings.HasPrefix(sub, prefix) {
				return sub
			}
		}
	}
	return ""
}

// --- helpers ---

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

func extractFirstLineFromToolResult(entry map[string]interface{}) string {
	message, ok := entry["message"].(map[string]interface{})
	if !ok {
		return ""
	}
	content, ok := message["content"].([]interface{})
	if !ok {
		return ""
	}
	for _, item := range content {
		block, isMap := item.(map[string]interface{})
		if !isMap || jsonutil.GetStr(block, "type") != "tool_result" {
			continue
		}
		sub := block["content"]
		if s, isStr := sub.(string); isStr && strings.TrimSpace(s) != "" {
			line := strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
			line = truncate(line, 80)
			return line
		}
	}
	return ""
}
