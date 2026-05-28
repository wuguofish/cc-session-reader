// Package session defines the normalized domain model used by the reader.
package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

type EventKind string

const (
	EventUserMessage      EventKind = "user_message"
	EventAssistantMessage EventKind = "assistant_message"
	EventToolResult       EventKind = "tool_result"
	EventNoise            EventKind = "noise"
)

type Event struct {
	Kind      EventKind
	Timestamp string
	RawType   string

	User      *UserMessage
	Assistant *AssistantMessage
	Tool      *ToolResult
	Noise     *NoiseEvent
}

type UserMessage struct {
	Text     string
	IsAnswer bool
}

type AssistantMessage struct {
	Text     string
	Thinking []string
	ToolUses []ToolUse
}

type ToolUse struct {
	ID    string
	Name  string
	Input ToolInput
}

type ToolInput struct {
	Raw map[string]any
}

func (i ToolInput) String(key string) string {
	if v, ok := i.Raw[key].(string); ok {
		return v
	}
	return ""
}

func (i ToolInput) MarshalNoEscape() string {
	if i.Raw == nil {
		return "{}"
	}
	return MarshalNoEscape(i.Raw)
}

// MarshalNoEscape JSON-encodes v without HTML escaping.
// Returns "{}" for nil values or encoding errors.
func MarshalNoEscape(v any) string {
	if v == nil {
		return "{}"
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "{}"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ToolShortID returns the last 4 characters of a tool_use_id as a short identifier.
func ToolShortID(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[len(id)-4:]
}

// ShortID truncates id to maxLen characters.
func ShortID(id string, maxLen int) string {
	if len(id) > maxLen {
		return id[:maxLen]
	}
	return id
}

type ToolResult struct {
	ToolUseID string
	Success   bool
	Text      string
	RawName   string
}

func (r ToolResult) Status() string {
	if r.Success {
		return "ok"
	}
	return "FAILED"
}

func (r ToolResult) Summary() string {
	firstLine := FirstLine(r.Text, 80)
	if firstLine != "" {
		return fmt.Sprintf(" -> %s: %s", r.Status(), firstLine)
	}
	return fmt.Sprintf(" -> %s", r.Status())
}

type NoiseEvent struct {
	Text string
}

func FirstLine(s string, maxRunes int) string {
	line := strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
	return Truncate(line, maxRunes)
}

func Truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}
