// Package main is the CLI entry point for the Claude session reader.
// Subcommands: list, read, context, stats, audit.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"claude-code-session-reader/internal/formatter"
	"claude-code-session-reader/internal/jsonutil"
	"claude-code-session-reader/internal/parser"
	"claude-code-session-reader/internal/summarizer"
	"claude-code-session-reader/internal/tokens"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcommand := os.Args[1]
	switch subcommand {
	case "list":
		cmdList(os.Args[2:])
	case "read":
		cmdRead(os.Args[2:])
	case "context":
		cmdContext(os.Args[2:])
	case "stats":
		cmdStats(os.Args[2:])
	case "audit":
		cmdAudit(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", subcommand)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: sessions <command> [options]")
	fmt.Fprintln(os.Stderr, "Commands: list, read, context, stats, audit")
}

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	limit := fs.Int("n", 20, "max sessions to display")
	project := fs.String("p", "", "filter by project name (case-insensitive)")
	_ = fs.Parse(args)

	metaFiles, err := parser.ListSessionMetaFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing sessions: %v\n", err)
		os.Exit(1)
	}

	projectFilter := ""
	if *project != "" {
		projectFilter = strings.ToLower(*project)
	}

	printed := 0
	for _, mf := range metaFiles {
		if printed >= *limit {
			break
		}

		data, err := os.ReadFile(mf.Path)
		if err != nil {
			continue
		}
		var meta map[string]interface{}
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		projectPath := jsonutil.GetStr(meta, "project_path")
		projectName := "?"
		if projectPath != "" {
			projectName = filepath.Base(projectPath)
		}

		if projectFilter != "" && !strings.Contains(strings.ToLower(projectName), projectFilter) {
			continue
		}

		sid := jsonutil.GetStr(meta, "session_id")
		if sid == "" {
			sid = strings.TrimSuffix(filepath.Base(mf.Path), ".json")
		}
		startTime := jsonutil.GetStr(meta, "start_time")
		duration := jsonutil.GetNum(meta, "duration_minutes")
		userMsgs := jsonutil.GetNum(meta, "user_message_count")
		asstMsgs := jsonutil.GetNum(meta, "assistant_message_count")
		firstPrompt := jsonutil.GetStr(meta, "first_prompt")
		runes := []rune(firstPrompt)
		if len(runes) > 80 {
			firstPrompt = string(runes[:77]) + "..."
		}

		dateStr := "??-??"
		if startTime != "" {
			dateStr = parser.FormatTimestamp(startTime)
		}

		fmt.Printf("%s  %s  %-20s  %3dm  u:%d a:%d  %s\n",
			sid, dateStr, projectName, duration, userMsgs, asstMsgs, firstPrompt)
		printed++
	}

	if printed == 0 {
		fmt.Fprintln(os.Stderr, "No sessions found.")
	}
}

func cmdRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	maxLines := fs.Int("max-lines", 0, "max output lines (0=unlimited)")
	isVerboseAgents := fs.Bool("verbose-agents", false, "show full agent results")
	_ = fs.Parse(reorderArgs(args))

	sessionID := resolveSessionArg(fs)
	transcriptPath := findTranscriptOrExit(sessionID)

	if err := formatter.FormatRead(transcriptPath, sessionID, *maxLines, *isVerboseAgents, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdContext(args []string) {
	fs := flag.NewFlagSet("context", flag.ExitOnError)
	isVerboseAgents := fs.Bool("verbose-agents", false, "show full agent results")
	_ = fs.Parse(reorderArgs(args))

	sessionID := resolveSessionArg(fs)
	transcriptPath := findTranscriptOrExit(sessionID)

	if err := formatter.FormatContext(transcriptPath, sessionID, *isVerboseAgents, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	isNoTokens := fs.Bool("no-tokens", false, "skip token counting")
	_ = fs.Parse(reorderArgs(args))

	sessionID := resolveSessionArg(fs)
	transcriptPath := findTranscriptOrExit(sessionID)

	entries, err := parser.ParseTranscript(transcriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing transcript: %v\n", err)
		os.Exit(1)
	}

	var rawParts []string
	var filteredParts []string
	categories := map[string]int{
		"user_text":       0,
		"user_answers":    0,
		"assistant_text":  0,
		"tool_summaries":  0,
		"tool_input_raw":  0,
		"tool_result_raw": 0,
		"system_noise":    0,
	}

	for _, entry := range entries {
		message, ok := entry["message"].(map[string]interface{})
		if !ok {
			continue
		}

		if parser.IsNoise(entry) {
			text := parser.ExtractAllText(entry)
			categories["system_noise"] += utf8.RuneCountInString(text)
			rawParts = append(rawParts, text)
			continue
		}

		content := message["content"]

		// Tool result entries
		if _, hasToolResult := entry["toolUseResult"]; hasToolResult {
			full := parser.ExtractAllText(entry)
			if summarizer.IsUserAnswer(entry) {
				answer := summarizer.ExtractUserAnswers(entry)
				categories["user_answers"] += utf8.RuneCountInString(answer)
				rawParts = append(rawParts, full)
				filteredParts = append(filteredParts, answer)
			} else {
				categories["tool_result_raw"] += utf8.RuneCountInString(full)
				rawParts = append(rawParts, full)
				summary := summarizer.SummarizeToolResult(entry)
				categories["tool_summaries"] += utf8.RuneCountInString(summary)
				filteredParts = append(filteredParts, summary)
			}
			continue
		}

		role := jsonutil.GetStr(message, "role")
		switch role {
		case "user":
			text := parser.ExtractText(content)
			if strings.TrimSpace(text) != "" {
				categories["user_text"] += utf8.RuneCountInString(text)
				rawParts = append(rawParts, text)
				filteredParts = append(filteredParts, text)
			}
		case "assistant":
			text := parser.ExtractText(content)
			if strings.TrimSpace(text) != "" {
				categories["assistant_text"] += utf8.RuneCountInString(text)
				rawParts = append(rawParts, text)
				filteredParts = append(filteredParts, text)
			}
			for _, tb := range parser.GetToolUses(content) {
				rawJSON := jsonutil.MarshalNoEscape(jsonutil.GetInputMap(tb))
				categories["tool_input_raw"] += utf8.RuneCountInString(rawJSON)
				rawParts = append(rawParts, rawJSON)

				name := jsonutil.GetStr(tb, "name")
				if name == "" {
					name = "?"
				}
				summary := summarizer.SummarizeToolUse(name, jsonutil.GetInputMap(tb))
				categories["tool_summaries"] += utf8.RuneCountInString(summary)
				filteredParts = append(filteredParts, summary)
			}
		}
	}

	rawText := strings.Join(rawParts, "\n")
	filteredText := strings.Join(filteredParts, "\n")
	rawC := utf8.RuneCountInString(rawText)
	filtC := utf8.RuneCountInString(filteredText)

	shortID := sessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	info, _ := os.Stat(transcriptPath)
	fileSize := float64(0)
	if info != nil {
		fileSize = float64(info.Size()) / 1024.0
	}

	fmt.Printf("Session: %s\n", shortID)
	fmt.Printf("Transcript: %.1fKB\n\n", fileSize)
	fmt.Println("=== Characters ===")
	fmt.Printf("  Raw:      %10s\n", formatNumber(rawC))
	fmt.Printf("  Filtered: %10s\n", formatNumber(filtC))
	if rawC > 0 {
		saved := rawC - filtC
		pct := float64(saved) * 100.0 / float64(rawC)
		fmt.Printf("  Saved:    %10s (%.1f%%)\n", formatNumber(saved), pct)
	}

	fmt.Println("\n=== Breakdown ===")
	breakdownLabels := []struct {
		label string
		key   string
	}{
		{"KEPT  user text:        ", "user_text"},
		{"KEPT  user answers:     ", "user_answers"},
		{"KEPT  assistant text:   ", "assistant_text"},
		{"KEPT  tool summaries:   ", "tool_summaries"},
		{"CUT   tool input (raw): ", "tool_input_raw"},
		{"CUT   tool result (raw):", "tool_result_raw"},
		{"CUT   system/noise:     ", "system_noise"},
	}
	for _, bl := range breakdownLabels {
		fmt.Printf("  %s %10s\n", bl.label, formatNumber(categories[bl.key]))
	}

	if *isNoTokens {
		return
	}

	fmt.Println()
	rawAPI, errRaw := tokens.CountTokensAPI(rawText)
	filtAPI, errFilt := tokens.CountTokensAPI(filteredText)
	if errRaw == nil && errFilt == nil {
		saved := rawAPI - filtAPI
		fmt.Println("=== Tokens (Anthropic API) ===")
		fmt.Printf("  Raw:      %10s\n", formatNumber(rawAPI))
		fmt.Printf("  Filtered: %10s\n", formatNumber(filtAPI))
		if rawAPI > 0 {
			pct := float64(saved) * 100.0 / float64(rawAPI)
			fmt.Printf("  Saved:    %10s (%.1f%%)\n", formatNumber(saved), pct)
		}
	} else {
		rawEst := tokens.EstimateTokens(rawText)
		filtEst := tokens.EstimateTokens(filteredText)
		savedEst := rawEst - filtEst
		fmt.Println("=== Tokens (estimated) ===")
		fmt.Printf("  Raw:      %10s ~\n", formatNumber(rawEst))
		fmt.Printf("  Filtered: %10s ~\n", formatNumber(filtEst))
		if rawEst > 0 {
			pct := float64(savedEst) * 100.0 / float64(rawEst)
			fmt.Printf("  Saved:    %10s ~ (%.1f%%)\n", formatNumber(savedEst), pct)
		}
	}
}

func cmdAudit(args []string) {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	samples := fs.Int("n", 5, "number of samples per category")
	_ = fs.Parse(reorderArgs(args))

	sessionID := resolveSessionArg(fs)
	transcriptPath := findTranscriptOrExit(sessionID)

	entries, err := parser.ParseTranscript(transcriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing transcript: %v\n", err)
		os.Exit(1)
	}

	categories := map[string][]string{
		"tool_result_cut": {},
		"system_noise":    {},
		"thinking":        {},
	}

	for _, entry := range entries {
		message, ok := entry["message"].(map[string]interface{})
		if !ok {
			continue
		}

		if parser.IsNoise(entry) {
			text := parser.ExtractAllText(entry)
			if strings.TrimSpace(text) != "" {
				entryType := jsonutil.GetStr(entry, "type")
				snippet := text
				if len(snippet) > 200 {
					snippet = snippet[:200]
				}
				categories["system_noise"] = append(categories["system_noise"],
					fmt.Sprintf("[%s] %s", entryType, snippet))
			}
			continue
		}

		// Tool result that is not a user answer
		if _, hasToolResult := entry["toolUseResult"]; hasToolResult {
			if !summarizer.IsUserAnswer(entry) {
				text := parser.ExtractAllText(entry)
				if strings.TrimSpace(text) != "" && len(text) > 100 {
					tr, ok := entry["toolUseResult"].(map[string]interface{})
					name := "?"
					if ok {
						if n := jsonutil.GetStr(tr, "commandName"); n != "" {
							name = n
						}
					}
					snippet := text
					if len(snippet) > 300 {
						snippet = snippet[:300]
					}
					categories["tool_result_cut"] = append(categories["tool_result_cut"],
						fmt.Sprintf("[%s] %s", name, snippet))
				}
			}
			continue
		}

		// Thinking blocks in assistant messages
		if jsonutil.GetStr(message, "role") == "assistant" {
			if content, ok := message["content"].([]interface{}); ok {
				for _, item := range content {
					block, isMap := item.(map[string]interface{})
					if !isMap || jsonutil.GetStr(block, "type") != "thinking" {
						continue
					}
					thinking := jsonutil.GetStr(block, "thinking")
					if strings.TrimSpace(thinking) != "" {
						snippet := thinking
						if len(snippet) > 300 {
							snippet = snippet[:300]
						}
						categories["thinking"] = append(categories["thinking"], snippet)
					}
				}
			}
		}
	}

	for _, catName := range []string{"tool_result_cut", "system_noise", "thinking"} {
		items := categories[catName]
		if len(items) == 0 {
			continue
		}
		shown := *samples
		if shown > len(items) {
			shown = len(items)
		}
		fmt.Printf("=== %s (%d items, showing %d) ===\n", catName, len(items), shown)
		for _, item := range items[:shown] {
			fmt.Printf("  %s\n\n", item)
		}
		if len(items) > shown {
			fmt.Printf("  ... and %d more\n\n", len(items)-shown)
		}
	}
}

// --- helpers ---

// reorderArgs moves flags before positional args so Go's flag package
// can parse them correctly. Go's flag.Parse stops at the first non-flag
// argument, but argparse (Python) allows intermixed flags and positionals.
// This makes `audit 3537152c -n 2` work the same as `audit -n 2 3537152c`.
func reorderArgs(args []string) []string {
	var flags []string
	var positional []string
	i := 0
	for i < len(args) {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			// Check if next arg is the flag's value (not another flag)
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.Contains(args[i], "=") {
				flags = append(flags, args[i+1])
				i += 2
			} else {
				i++
			}
		} else {
			positional = append(positional, args[i])
			i++
		}
	}
	return append(flags, positional...)
}

func resolveSessionArg(fs *flag.FlagSet) string {
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: session_id is required\n")
		os.Exit(1)
	}
	sessionID, err := parser.ResolveSessionID(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	return sessionID
}

func findTranscriptOrExit(sessionID string) string {
	path := parser.FindTranscript(sessionID)
	if path == "" {
		fmt.Fprintf(os.Stderr, "Transcript not found: %s\n", sessionID)
		os.Exit(1)
	}
	return path
}

func formatNumber(n int) string {
	if n < 0 {
		return "-" + formatNumber(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
