package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mapleeeeeeeeeee/cc-session-reader/internal/analyzer"
	"github.com/Mapleeeeeeeeeee/cc-session-reader/internal/parser"
)

func TestPrintCompressionSection_WhenRendered_ThenUsesNewSessionTotalContext(t *testing.T) {
	results := []sessionBenchResult{
		{
			shortID:          "aaaaaaaa",
			contextTokens:    100_000,
			filteredTokens:   23_456,
			newContextTokens: 60_000,
			savedPct:         40.0,
		},
		{
			shortID:          "bbbbbbbb",
			contextTokens:    200_000,
			filteredTokens:   87_654,
			newContextTokens: 120_000,
			savedPct:         40.0,
		},
	}

	var out bytes.Buffer
	printCompressionSection(&out, results)
	got := out.String()

	if !strings.Contains(got, "Context      NewCtx") {
		t.Fatalf("compression header must compare total contexts, got:\n%s", got)
	}
	if strings.Contains(got, "Filtered") {
		t.Fatalf("compression table must not label history-only tokens as the comparable total context:\n%s", got)
	}
	if !strings.Contains(got, "aaaaaaaa") ||
		!strings.Contains(got, "100,000") ||
		!strings.Contains(got, "60,000") ||
		!strings.Contains(got, "40.0%") {
		t.Fatalf("compression row missing new session total context:\n%s", got)
	}
	if strings.Contains(got, "23,456") || strings.Contains(got, "87,654") {
		t.Fatalf("compression row leaked filtered-history-only token count:\n%s", got)
	}
}

func TestMedianFloat64_GivenEvenCount_ThenAveragesMiddleValues(t *testing.T) {
	got := medianFloat64([]float64{78.9, 90.3})
	want := 84.6
	if got != want {
		t.Fatalf("medianFloat64(even) = %.1f, want %.1f", got, want)
	}
}

func TestPrintCompressionSection_GivenEvenCount_ThenPrintsAveragedMedian(t *testing.T) {
	results := []sessionBenchResult{
		{shortID: "aaaaaaaa", contextTokens: 100, newContextTokens: 20, savedPct: 78.9},
		{shortID: "bbbbbbbb", contextTokens: 100, newContextTokens: 10, savedPct: 90.3},
	}

	var out bytes.Buffer
	printCompressionSection(&out, results)

	if got := out.String(); !strings.Contains(got, "Median: 84.6%") {
		t.Fatalf("compression summary must average the two middle values for even counts:\n%s", got)
	}
}

func TestRunBenchmark_WhenSessionHasAPIUsage_ThenUsesTokenCountingAPIForNewContext(t *testing.T) {
	root := t.TempDir()
	sid := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	projectDir := filepath.Join(root, "projects", "proj")
	metaDir := filepath.Join(root, "session-meta")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("create meta dir: %v", err)
	}

	transcript := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-28T00:00:00Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"Bash","id":"toolu_1","input":{"command":"echo raw payload that must not be counted"}}],"usage":{"input_tokens":100000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1000}}}`,
		`{"type":"user","timestamp":"2026-05-28T00:00:02Z","toolUseResult":{"success":true,"commandName":"Bash"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"raw result that is replaced by a summary"}]}}`,
		"",
	}, "\n")
	transcriptPath := filepath.Join(projectDir, sid+".jsonl")
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	writeListMeta(t, metaDir, sid, "/tmp/proj", "hello")
	stats := analyzer.ComputeStats(mustReadAll(t, transcriptPath))
	if stats.RawText == stats.FilteredText {
		t.Fatal("fixture invalid: raw and filtered text must differ")
	}

	const filteredTokenCount = 23_456
	original := newCountTokensFn
	t.Cleanup(func() { newCountTokensFn = original })
	var countedText string
	newCountTokensFn = func() (countTokensFunc, error) {
		return func(text string) (int, error) {
			countedText = text
			if text != stats.FilteredText {
				t.Fatalf("token counter received wrong text; got raw=%t filtered=%t", text == stats.RawText, text == stats.FilteredText)
			}
			return filteredTokenCount, nil
		}, nil
	}

	var stdout, stderr bytes.Buffer
	store := parser.Store{ProjectsDir: filepath.Join(root, "projects"), SessionMetaDir: metaDir}
	if err := runBenchmark([]string{"--n", "1", "--min-kb", "0", "--overhead", "40000"}, &stdout, &stderr, store, testReader); err != nil {
		t.Fatalf("runBenchmark returned error: %v", err)
	}

	if countedText == "" {
		t.Fatal("countTokensFn was not called")
	}
	got := stdout.String()
	row := outputLineContaining(got, "aaaaaaaa")
	for _, want := range []string{"100,000", analyzer.FormatNumber(40_000 + filteredTokenCount), "36.5%"} {
		if !strings.Contains(row, want) {
			t.Fatalf("benchmark row missing %s:\nrow: %s\nfull output:\n%s", want, row, got)
		}
	}
}

func TestRunBenchmark_WhenSessionHasNoAPIUsage_ThenSkipsSession(t *testing.T) {
	root := t.TempDir()
	sid := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	projectDir := filepath.Join(root, "projects", "proj")
	metaDir := filepath.Join(root, "session-meta")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("create meta dir: %v", err)
	}

	transcript := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-28T00:00:00Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T00:00:01Z","message":{"role":"assistant","content":"hi"}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(projectDir, sid+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	writeListMeta(t, metaDir, sid, "/tmp/proj", "hello")

	original := newCountTokensFn
	t.Cleanup(func() { newCountTokensFn = original })
	newCountTokensFn = func() (countTokensFunc, error) {
		t.Fatal("newCountTokensFn must not be called for sessions without API usage data")
		return nil, nil
	}

	var stdout, stderr bytes.Buffer
	store := parser.Store{ProjectsDir: filepath.Join(root, "projects"), SessionMetaDir: metaDir}
	if err := runBenchmark([]string{"--n", "1", "--min-kb", "0"}, &stdout, &stderr, store, testReader); err != nil {
		t.Fatalf("runBenchmark returned error: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "missing API usage data") || !strings.Contains(got, "No sessions could be processed.") {
		t.Fatalf("benchmark output should skip sessions without API usage data:\n%s", got)
	}
}

func TestRunBenchmark_WhenTopCandidateIsSkipped_ThenNCountsProcessedResults(t *testing.T) {
	root := t.TempDir()
	metaDir := filepath.Join(root, "session-meta")
	projectDir := filepath.Join(root, "projects", "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("create meta dir: %v", err)
	}

	skippedSID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	skippedTranscript := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-28T00:00:00Z","message":{"role":"user","content":"` + strings.Repeat("old session without usage ", 200) + `"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T00:00:01Z","message":{"role":"assistant","content":"hi"}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(projectDir, skippedSID+".jsonl"), []byte(skippedTranscript), 0o644); err != nil {
		t.Fatalf("write skipped transcript: %v", err)
	}
	writeListMeta(t, metaDir, skippedSID, "/tmp/proj", "old")

	validSID := "dddddddd-dddd-dddd-dddd-dddddddddddd"
	validTranscript := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-28T00:00:00Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T00:00:01Z","message":{"role":"assistant","content":"hi","usage":{"input_tokens":100000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1000}}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(projectDir, validSID+".jsonl"), []byte(validTranscript), 0o644); err != nil {
		t.Fatalf("write valid transcript: %v", err)
	}
	writeListMeta(t, metaDir, validSID, "/tmp/proj", "new")

	original := newCountTokensFn
	t.Cleanup(func() { newCountTokensFn = original })
	newCountTokensFn = func() (countTokensFunc, error) {
		return func(text string) (int, error) {
			return 20_000, nil
		}, nil
	}

	var stdout, stderr bytes.Buffer
	store := parser.Store{ProjectsDir: filepath.Join(root, "projects"), SessionMetaDir: metaDir}
	if err := runBenchmark([]string{"--n", "1", "--min-kb", "0", "--overhead", "40000"}, &stdout, &stderr, store, testReader); err != nil {
		t.Fatalf("runBenchmark returned error: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "skipping cccccccc") || !strings.Contains(got, "dddddddd") {
		t.Fatalf("benchmark should skip invalid candidate and still process one valid result:\n%s", got)
	}
	if strings.Contains(got, "No sessions could be processed.") {
		t.Fatalf("benchmark incorrectly let skipped candidate consume --n:\n%s", got)
	}
}

func outputLineContaining(output string, needle string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
