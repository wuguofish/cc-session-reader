package formatter

import (
	"fmt"
	"io"
	"strings"

	"github.com/Mapleeeeeeeeeee/cc-session-reader/internal/session"
	"github.com/Mapleeeeeeeeeee/cc-session-reader/internal/summarizer"
)

// FormatOptions controls verbosity for formatting functions.
type FormatOptions struct {
	VerboseAgents   bool
	VerboseBash     bool
	VerboseThinking bool
	VerboseCommands bool
}

// userRender is the rendered form of a user-message event: the body to print
// and whether anything should be printed at all.
type userRender struct {
	body string
	show bool
}

// renderUserMessage resolves how a user-message event should appear given the
// verbosity options. It is the single rendering policy shared by read and
// context so both stay consistent:
//   - command invocation -> always show the marker (e.g. "[/context]")
//   - command noise -> drop by default; show ANSI-stripped body under
//     -verbose-commands, except caveats which are always dropped
//   - plain typed message -> show verbatim
func renderUserMessage(user *session.UserMessage, opts FormatOptions, seenSkills map[string]bool) userRender {
	if user == nil {
		return userRender{}
	}
	if user.CommandMarker != "" {
		return userRender{body: user.CommandMarker, show: true}
	}
	if user.IsCommandNoise {
		if !opts.VerboseCommands || user.IsCaveat {
			return userRender{}
		}
		body := strings.TrimSpace(session.StripANSI(user.Text))
		if body == "" {
			return userRender{}
		}
		return userRender{body: body, show: true}
	}

	// Harness-injected subtypes: strip or compact.
	if user.IsSystemReminder || user.IsContextUsage {
		return userRender{}
	}
	if user.IsSkillInjection {
		return userRender{body: session.CompactSkillInjection(user, seenSkills), show: true}
	}
	if user.IsTeammateMessage {
		if body, ok := session.CompactTeammateMessage(user.Text); ok {
			return userRender{body: body, show: true}
		}
		return userRender{body: user.Text, show: true}
	}
	if user.IsCommandInjection {
		if body, ok := session.CompactCommandInjection(user.Text); ok {
			return userRender{body: body, show: true}
		}
		return userRender{body: user.Text, show: true}
	}

	if strings.TrimSpace(user.Text) == "" {
		return userRender{}
	}
	if body, ok := session.CompactTaskNotification(user.Text); ok {
		return userRender{body: body, show: true}
	}
	return userRender{body: user.Text, show: true}
}

type pendingTool struct {
	summary string
	name    string // e.g. "Bash", "Read", "Edit"
}

func loadEvents(transcriptPath string, isVerboseAgents bool, reader session.TranscriptReader) ([]session.Event, map[string]bool, error) {
	events, err := reader.ReadAll(transcriptPath)
	if err != nil {
		return nil, nil, err
	}
	agentIDs := map[string]bool{}
	if isVerboseAgents {
		agentIDs = session.CollectAgentToolIDs(events)
	}
	return events, agentIDs, nil
}

func appendToolResult(result *session.ToolResult, pendingTools *[]pendingTool, opts FormatOptions) {
	if len(*pendingTools) > 0 {
		last := &(*pendingTools)[len(*pendingTools)-1]
		if opts.VerboseBash && last.name == session.ToolBash {
			last.summary += formatVerboseBashResult(result)
			return
		}
		last.summary += result.Summary()
		return
	}
	name := result.RawName
	if name == "" {
		name = "ToolResult"
	}
	summary := fmt.Sprintf("[%s]%s", name, result.Summary())
	if opts.VerboseBash && name == session.ToolBash {
		summary = fmt.Sprintf("[%s]%s", name, formatVerboseBashResult(result))
	}
	*pendingTools = append(*pendingTools, pendingTool{
		summary: summary,
		name:    name,
	})
}

func formatVerboseBashResult(result *session.ToolResult) string {
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return fmt.Sprintf(" -> %s", result.Status())
	}
	indented := indentBlock(text, "    ")
	return fmt.Sprintf(" -> %s:\n%s", result.Status(), indented)
}

func indentBlock(text string, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func summarizeToolUse(tool session.ToolUse) pendingTool {
	name := tool.Name
	if name == "" {
		name = "?"
	}
	shortID := session.ToolShortID(tool.ID)
	summary := summarizer.SummarizeToolUse(name, tool.Input)
	// Inject "#shortID" before the closing ']' of the first bracket group
	// so "[Bash] cmd" becomes "[Bash#ol-1] cmd" and
	// "[Agent(general)] desc" becomes "[Agent(general)#ol-1] desc".
	tagged := injectShortID(summary, shortID)
	return pendingTool{
		summary: tagged,
		name:    name,
	}
}

// injectShortID inserts "#id" before the first ']' in summary.
// "[Bash] Run tests" -> "[Bash#uCVa] Run tests"
// "[Agent(general)] Inspect" -> "[Agent(general)#uCVa] Inspect"
func injectShortID(summary string, shortID string) string {
	if shortID == "" {
		return summary
	}
	idx := strings.Index(summary, "]")
	if idx < 0 {
		return summary
	}
	return summary[:idx] + "#" + shortID + summary[idx:]
}

// applyPagination slices the formatted output by offset and maxLines, writing
// the result to out. It appends a truncation message when lines were cut.
func applyPagination(formatted string, maxLines int, offset int, out io.Writer) error {
	allLines := strings.Split(formatted, "\n")
	// strings.Split on a trailing newline produces an empty last element; exclude it
	// from the count so line math matches what the user sees.
	totalLines := len(allLines)
	if totalLines > 0 && allLines[totalLines-1] == "" {
		totalLines--
	}

	if offset >= totalLines {
		if totalLines > 0 {
			fmt.Fprintf(out, "--- offset %d exceeds total ~%d lines ---\n", offset, totalLines)
		}
		return nil
	}

	visibleLines := allLines[offset:]
	isTruncated := false
	if maxLines > 0 && len(visibleLines) > maxLines {
		visibleLines = visibleLines[:maxLines]
		isTruncated = true
	}

	fmt.Fprint(out, strings.Join(visibleLines, "\n"))
	// Restore the trailing newline that strings.Split consumed, unless the last
	// visible line is already empty (which would produce a double newline).
	lastVisible := visibleLines[len(visibleLines)-1]
	if lastVisible != "" {
		fmt.Fprintln(out)
	}

	if isTruncated {
		resumeAt := offset + maxLines
		fmt.Fprintf(out, "\n--- truncated at line %d (total ~%d lines) — use --offset %d to continue ---\n", resumeAt, totalLines, resumeAt)
	}
	return nil
}
