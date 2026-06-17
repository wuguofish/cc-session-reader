// Package session defines the normalized domain model used by the reader.
package session

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ansiEscapePattern matches ANSI/VT100 escape sequences: an ESC (\x1b)
// followed by a CSI sequence "[ ... <final-byte>" (covers SGR colour codes
// like "\x1b[38;2;136;136;136m" and "\x1b[1m"/"\x1b[22m"/"\x1b[39m"), or a
// single two-character escape. Content characters such as the "⛁ ⛶" box
// glyphs are not escape codes and are left untouched.
var ansiEscapePattern = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|[@-Z\\-_])`)

// StripANSI removes terminal control sequences from s, leaving printable
// content intact. Used when rendering command output bodies in verbose mode.
func StripANSI(s string) string {
	return ansiEscapePattern.ReplaceAllString(s, "")
}

type EventKind string

const (
	EventUserMessage      EventKind = "user_message"
	EventAssistantMessage EventKind = "assistant_message"
	EventToolResult       EventKind = "tool_result"
	EventNoise            EventKind = "noise"
)

// Tool names. Single source of truth for tool name literals shared across the
// summarizer, formatter, and claudecodec packages.
const (
	ToolBash            = "Bash"
	ToolRead            = "Read"
	ToolEdit            = "Edit"
	ToolWrite           = "Write"
	ToolAgent           = "Agent"
	ToolGrep            = "Grep"
	ToolGlob            = "Glob"
	ToolSkill           = "Skill"
	ToolAskUserQuestion = "AskUserQuestion"
	ToolSearch          = "ToolSearch"
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

	// CommandMarker is the one-line representation of a slash- or bang-command
	// invocation, e.g. "[/context]" or "[!ls -la]". Empty for plain user
	// messages and for command output. When set, formatters render the marker
	// instead of Text regardless of verbosity.
	CommandMarker string

	// IsCommandNoise marks machine-generated command output that Claude Code
	// stores as a user-role entry (<local-command-stdout>, <bash-stdout>,
	// <bash-stderr>, <local-command-caveat>). The body is dropped by default
	// and only shown under -verbose-commands.
	IsCommandNoise bool

	// IsCaveat marks the boilerplate <local-command-caveat> disclaimer. It is
	// dropped unconditionally (zero information even in verbose mode).
	IsCaveat bool

	// IsSkillInjection marks a user message that injects a SKILL.md body.
	// The SkillName field carries the extracted skill name for compact rendering.
	IsSkillInjection bool
	SkillName        string
	SkillArgs        string

	// IsTeammateMessage marks a teammate-message with harness warning boilerplate.
	IsTeammateMessage bool

	// IsCommandInjection marks a <command-message>/<command-name> XML block
	// that precedes a skill injection (distinct from the existing CommandMarker
	// which covers slash-commands detected via <command-name> at line start).
	IsCommandInjection bool

	// IsContextUsage marks a /context output block (runtime token table).
	IsContextUsage bool

	// IsSystemReminder marks a <system-reminder> harness injection.
	IsSystemReminder bool
}

type Usage struct {
	InputTokens              int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	OutputTokens             int
}

// ContextTokens returns the total context window size for this API call:
// direct input plus both cache layers.
func (u Usage) ContextTokens() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

func (u *Usage) Equal(other *Usage) bool {
	if u == nil || other == nil {
		return u == other
	}
	return u.InputTokens == other.InputTokens &&
		u.CacheCreationInputTokens == other.CacheCreationInputTokens &&
		u.CacheReadInputTokens == other.CacheReadInputTokens &&
		u.OutputTokens == other.OutputTokens
}

type AssistantMessage struct {
	Text     string
	Thinking []string
	ToolUses []ToolUse
	Usage    *Usage
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
	// Byte length >= rune count, so a string within maxRunes bytes is
	// guaranteed within maxRunes runes — a fast early return that avoids
	// allocating a rune slice for the common short-string case.
	if len(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// CompactTaskNotification strips XML boilerplate from task-notification
// messages, keeping only the summary and result content. Returns the
// compacted text and true, or ("", false) if the input is not a
// task-notification.
func CompactTaskNotification(text string) (string, bool) {
	if !strings.Contains(text, "<task-notification>") {
		return "", false
	}
	summary := extractXMLTag(text, "summary")
	result := extractXMLTag(text, "result")
	if summary == "" && result == "" {
		return "", false
	}
	var b strings.Builder
	if summary != "" {
		b.WriteString("[" + summary + "]\n")
	}
	if result != "" {
		b.WriteString(result)
	}
	return strings.TrimSpace(b.String()), true
}

// CompactSkillInjection returns a one-line summary of a SKILL.md injection.
// seenSkills tracks which skills have appeared; repeats get a shorter form.
func CompactSkillInjection(user *UserMessage, seenSkills map[string]bool) string {
	repeat := seenSkills[user.SkillName]
	seenSkills[user.SkillName] = true
	if user.SkillArgs != "" {
		if repeat {
			return fmt.Sprintf("[skill: %s] (repeat) %s", user.SkillName, user.SkillArgs)
		}
		return fmt.Sprintf("[skill: %s] %s", user.SkillName, user.SkillArgs)
	}
	if repeat {
		return fmt.Sprintf("[skill: %s] (repeat)", user.SkillName)
	}
	return fmt.Sprintf("[skill: %s]", user.SkillName)
}

// CompactTeammateMessage strips the harness warning boilerplate from a
// teammate message, keeping only the teammate ID, summary, and body content.
func CompactTeammateMessage(text string) (string, bool) {
	if !strings.Contains(text, "<teammate-message") {
		return "", false
	}

	// Strip the warning boilerplate.
	const warningPrefix = "\n\nIMPORTANT: This is NOT from your user"
	if idx := strings.Index(text, warningPrefix); idx >= 0 {
		text = text[:idx]
	}

	// May contain multiple <teammate-message> blocks.
	var parts []string
	remaining := text
	for {
		openIdx := strings.Index(remaining, "<teammate-message")
		if openIdx < 0 {
			break
		}
		// Extract attributes from the opening tag.
		tagEnd := strings.Index(remaining[openIdx:], ">")
		if tagEnd < 0 {
			break
		}
		openingTag := remaining[openIdx : openIdx+tagEnd+1]
		tmID := extractXMLAttr(openingTag, "teammate_id")
		summary := extractXMLAttr(openingTag, "summary")

		// Extract body between > and </teammate-message>.
		bodyStart := openIdx + tagEnd + 1
		closeTag := "</teammate-message>"
		closeIdx := strings.Index(remaining[bodyStart:], closeTag)
		if closeIdx < 0 {
			break
		}
		body := strings.TrimSpace(remaining[bodyStart : bodyStart+closeIdx])

		// Format the compact line.
		var line string
		if isIdleNotification(body) {
			line = fmt.Sprintf("[teammate: %s] idle", tmID)
		} else if summary != "" {
			line = fmt.Sprintf("[teammate: %s %q]\n%s", tmID, summary, body)
		} else {
			line = fmt.Sprintf("[teammate: %s]\n%s", tmID, body)
		}
		parts = append(parts, line)

		remaining = remaining[bodyStart+closeIdx+len(closeTag):]
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n\n"), true
}

func isIdleNotification(body string) bool {
	return strings.Contains(body, `"idle_notification"`) ||
		(strings.Contains(body, `"idleReason"`) && len(body) < 300)
}

func extractXMLAttr(tag, attr string) string {
	key := attr + `="`
	idx := strings.Index(tag, key)
	if idx < 0 {
		return ""
	}
	start := idx + len(key)
	end := strings.Index(tag[start:], `"`)
	if end < 0 {
		return ""
	}
	return tag[start : start+end]
}

// CompactCommandInjection extracts the command name and args from a
// <command-message>/<command-name>/<command-args> XML block into a single line.
func CompactCommandInjection(text string) (string, bool) {
	name := extractXMLTag(text, "command-name")
	args := extractXMLTag(text, "command-args")
	if name == "" {
		return "", false
	}
	name = strings.TrimSpace(name)
	args = strings.TrimSpace(args)
	if args != "" {
		return name + " " + args, true
	}
	return name, true
}

// CollectAgentToolIDs returns a set of tool_use_ids from Agent tool invocations
// in the given events. Used by formatters to identify agent results.
func CollectAgentToolIDs(events []Event) map[string]bool {
	ids := make(map[string]bool)
	for _, event := range events {
		if event.Assistant == nil {
			continue
		}
		for _, tool := range event.Assistant.ToolUses {
			if tool.Name == ToolAgent && tool.ID != "" {
				ids[tool.ID] = true
			}
		}
	}
	return ids
}

func extractXMLTag(text, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(text, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(text[start:], close)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}
