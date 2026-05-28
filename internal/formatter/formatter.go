// Package formatter handles read and context output formatting with streaming JSONL processing.
package formatter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"claude-code-session-reader/internal/jsonutil"
	"claude-code-session-reader/internal/parser"
	"claude-code-session-reader/internal/summarizer"
)

// FormatRead streams a transcript in detailed read format:
// [MM-DD HH:MM] role:\n{text}\n\n with [Tool] summary -> status for tools.
func FormatRead(transcriptPath string, sessionID string, maxLines int, isVerboseAgents bool, out io.Writer) error {
	var agentIDs map[string]bool
	if isVerboseAgents {
		var err error
		agentIDs, err = parser.CollectAgentToolIDs(transcriptPath)
		if err != nil {
			return fmt.Errorf("collect agent IDs: %w", err)
		}
	}

	f, err := os.Open(transcriptPath)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	linesOutput := 0
	var pendingTools []string

	flush := func() {
		for _, s := range pendingTools {
			fmt.Fprintf(out, "  %s\n", s)
			linesOutput++
		}
		if len(pendingTools) > 0 {
			fmt.Fprintln(out)
			linesOutput++
		}
		pendingTools = pendingTools[:0]
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 64*1024*1024)

	for scanner.Scan() {
		if maxLines > 0 && linesOutput >= maxLines {
			fmt.Fprintf(out, "\n--- truncated at %d output lines ---\n", maxLines)
			break
		}

		entry := safeParse(scanner.Bytes())
		if entry == nil || parser.IsNoise(entry) {
			continue
		}

		message, hasMessage := entry["message"].(map[string]interface{})
		if !hasMessage {
			continue
		}

		// Handle tool results
		if _, hasToolResult := entry["toolUseResult"]; hasToolResult {
			handleToolResultRead(entry, agentIDs, &pendingTools, flush, out, &linesOutput)
			continue
		}

		role := jsonutil.GetStr(message, "role")
		content := message["content"]
		ts := parser.FormatTimestamp(jsonutil.GetStr(entry, "timestamp"))

		switch role {
		case "user":
			flush()
			text := parser.ExtractText(content)
			if strings.TrimSpace(text) == "" {
				continue
			}
			fmt.Fprintf(out, "[%s] user:\n%s\n\n", ts, text)
			linesOutput += strings.Count(text, "\n") + 3

		case "assistant":
			text := parser.ExtractText(content)
			toolBlocks := parser.GetToolUses(content)

			hasText := strings.TrimSpace(text) != ""
			hasTools := len(toolBlocks) > 0

			if !hasText && !hasTools {
				continue
			}

			if hasText {
				flush()
				fmt.Fprintf(out, "[%s] assistant:\n%s\n", ts, text)
				linesOutput += strings.Count(text, "\n") + 2
			}

			for _, tb := range toolBlocks {
				name := jsonutil.GetStr(tb, "name")
				if name == "" {
					name = "?"
				}
				inp := jsonutil.GetInputMap(tb)
				pendingTools = append(pendingTools, summarizer.SummarizeToolUse(name, inp))
			}

			if hasText && !hasTools {
				fmt.Fprintln(out)
				linesOutput++
			}
		}
	}

	flush()

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan transcript: %w", err)
	}
	return nil
}

func handleToolResultRead(
	entry map[string]interface{},
	agentIDs map[string]bool,
	pendingTools *[]string,
	flushFn func(),
	out io.Writer,
	linesOutput *int,
) {
	if summarizer.IsUserAnswer(entry) {
		flushFn()
		ts := parser.FormatTimestamp(jsonutil.GetStr(entry, "timestamp"))
		answer := summarizer.ExtractUserAnswers(entry)
		fmt.Fprintf(out, "[%s] user (answer):\n%s\n\n", ts, answer)
		*linesOutput += strings.Count(answer, "\n") + 3
		return
	}

	if len(agentIDs) > 0 {
		fullText, toolUseID := parser.ExtractToolResultText(entry)
		if agentIDs[toolUseID] && strings.TrimSpace(fullText) != "" {
			flushFn()
			ts := parser.FormatTimestamp(jsonutil.GetStr(entry, "timestamp"))
			fmt.Fprintf(out, "[%s] agent result:\n%s\n\n", ts, fullText)
			*linesOutput += strings.Count(fullText, "\n") + 3
			return
		}
	}

	resultStr := summarizer.SummarizeToolResult(entry)
	if len(*pendingTools) > 0 {
		(*pendingTools)[len(*pendingTools)-1] += resultStr
	}
}

// FormatContext streams a transcript in compact context format:
// U: {text}\n\n / A: {text}\n\n with [Tool] summary lines.
func FormatContext(transcriptPath string, sessionID string, isVerboseAgents bool, out io.Writer) error {
	var agentIDs map[string]bool
	if isVerboseAgents {
		var err error
		agentIDs, err = parser.CollectAgentToolIDs(transcriptPath)
		if err != nil {
			return fmt.Errorf("collect agent IDs: %w", err)
		}
	}

	// Print session header from metadata
	meta, err := parser.LoadSessionMeta(sessionID)
	if err == nil && meta != nil {
		projectPath := jsonutil.GetStr(meta, "project_path")
		project := filepath.Base(projectPath)
		if project == "" || project == "." {
			project = "?"
		}
		duration := "?"
		if d, ok := meta["duration_minutes"]; ok {
			duration = fmt.Sprintf("%v", d)
		}
		shortID := sessionID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		fmt.Fprintf(out, "# Session %s | %s | %sm\n\n", shortID, project, duration)
	}

	f, err := os.Open(transcriptPath)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	var pendingTools []string

	flush := func() {
		for _, s := range pendingTools {
			fmt.Fprintf(out, "  %s\n", s)
		}
		if len(pendingTools) > 0 {
			fmt.Fprintln(out)
		}
		pendingTools = pendingTools[:0]
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 64*1024*1024)

	for scanner.Scan() {
		entry := safeParse(scanner.Bytes())
		if entry == nil || parser.IsNoise(entry) {
			continue
		}

		message, hasMessage := entry["message"].(map[string]interface{})
		if !hasMessage {
			continue
		}

		// Handle tool results
		if _, hasToolResult := entry["toolUseResult"]; hasToolResult {
			if summarizer.IsUserAnswer(entry) {
				flush()
				answer := summarizer.ExtractUserAnswers(entry)
				fmt.Fprintf(out, "U (answer): %s\n\n", answer)
			} else if len(agentIDs) > 0 {
				fullText, toolUseID := parser.ExtractToolResultText(entry)
				if agentIDs[toolUseID] && strings.TrimSpace(fullText) != "" {
					flush()
					fmt.Fprintf(out, "Agent result:\n%s\n\n", fullText)
				} else {
					resultStr := summarizer.SummarizeToolResult(entry)
					if len(pendingTools) > 0 {
						pendingTools[len(pendingTools)-1] += resultStr
					}
				}
			} else {
				resultStr := summarizer.SummarizeToolResult(entry)
				if len(pendingTools) > 0 {
					pendingTools[len(pendingTools)-1] += resultStr
				}
			}
			continue
		}

		role := jsonutil.GetStr(message, "role")
		content := message["content"]

		switch role {
		case "user":
			flush()
			text := parser.ExtractText(content)
			if strings.TrimSpace(text) != "" {
				fmt.Fprintf(out, "U: %s\n\n", text)
			}

		case "assistant":
			text := parser.ExtractText(content)
			toolBlocks := parser.GetToolUses(content)

			if strings.TrimSpace(text) != "" {
				flush()
				fmt.Fprintf(out, "A: %s\n\n", text)
			}

			for _, tb := range toolBlocks {
				name := jsonutil.GetStr(tb, "name")
				if name == "" {
					name = "?"
				}
				inp := jsonutil.GetInputMap(tb)
				pendingTools = append(pendingTools, summarizer.SummarizeToolUse(name, inp))
			}
		}
	}

	flush()

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan transcript: %w", err)
	}
	return nil
}

// --- helpers ---

func safeParse(data []byte) map[string]interface{} {
	var entry map[string]interface{}
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil
	}
	return entry
}
