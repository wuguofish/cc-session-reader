// Package parser handles transcript I/O, session discovery, and content extraction.
package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"claude-code-session-reader/internal/jsonutil"
)

// Directory constants derived from ~/.claude/
var (
	ClaudeDir      = filepath.Join(homeDir(), ".claude")
	ProjectsDir    = filepath.Join(ClaudeDir, "projects")
	SessionMetaDir = filepath.Join(ClaudeDir, "usage-data", "session-meta")
)

// NoiseTypes are entry types filtered out during transcript processing.
var NoiseTypes = map[string]bool{
	"file-history-snapshot": true,
	"attachment":            true,
	"bridge-session":        true,
	"last-prompt":           true,
	"permission-mode":       true,
	"ai-title":              true,
	"queue-operation":       true,
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// FindTranscript locates a transcript JSONL file by session ID under ~/.claude/projects/.
func FindTranscript(sessionID string) string {
	var found string
	_ = filepath.Walk(ProjectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == sessionID+".jsonl" {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// LoadSessionMeta reads session metadata from the session-meta directory.
func LoadSessionMeta(sessionID string) (map[string]interface{}, error) {
	metaFile := filepath.Join(SessionMetaDir, sessionID+".json")
	data, err := os.ReadFile(metaFile)
	if err != nil {
		return nil, err
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse session meta %s: %w", sessionID, err)
	}
	return meta, nil
}

// ResolveSessionID resolves a prefix to a full session UUID.
// If the prefix is already 36 chars (full UUID), it is returned as-is.
// Returns an error if the prefix is ambiguous.
func ResolveSessionID(prefix string) (string, error) {
	if len(prefix) == 36 {
		return prefix, nil
	}

	var matches []string
	_ = filepath.Walk(ProjectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".jsonl" {
			stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
			if strings.HasPrefix(stem, prefix) {
				matches = append(matches, stem)
			}
		}
		return nil
	})

	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		shown := matches
		if len(shown) > 5 {
			shown = shown[:5]
		}
		shortIDs := make([]string, len(shown))
		for i, m := range shown {
			if len(m) >= 12 {
				shortIDs[i] = m[:12]
			} else {
				shortIDs[i] = m
			}
		}
		return "", fmt.Errorf("ambiguous prefix '%s', matches: %s", prefix, strings.Join(shortIDs, ", "))
	}
	return prefix, nil
}

// ParseTranscript reads all JSONL entries from a transcript file into memory.
func ParseTranscript(path string) ([]map[string]interface{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	var entries []map[string]interface{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 64*1024*1024)
	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	return entries, nil
}

// CollectAgentToolIDs pre-scans a transcript to find tool_use blocks with name "Agent",
// collecting their IDs for verbose agent output matching.
func CollectAgentToolIDs(path string) (map[string]bool, error) {
	ids := make(map[string]bool)
	f, err := os.Open(path)
	if err != nil {
		return ids, fmt.Errorf("open transcript for agent scan: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 64*1024*1024)
	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if jsonutil.GetStr(entry, "type") != "assistant" {
			continue
		}
		message := jsonutil.GetMap(entry, "message")
		if message == nil {
			continue
		}
		content, ok := message["content"].([]interface{})
		if !ok {
			continue
		}
		for _, item := range content {
			block, isMap := item.(map[string]interface{})
			if !isMap {
				continue
			}
			if jsonutil.GetStr(block, "type") == "tool_use" && jsonutil.GetStr(block, "name") == "Agent" {
				ids[jsonutil.GetStr(block, "id")] = true
			}
		}
	}
	return ids, nil
}

// SessionMetaFile holds metadata about a session, used for listing.
type SessionMetaFile struct {
	Path    string
	ModTime time.Time
}

// ListSessionMetaFiles returns session meta files sorted by modification time (newest first).
func ListSessionMetaFiles() ([]SessionMetaFile, error) {
	entries, err := os.ReadDir(SessionMetaDir)
	if err != nil {
		return nil, fmt.Errorf("read session meta dir: %w", err)
	}

	var files []SessionMetaFile
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, SessionMetaFile{
			Path:    filepath.Join(SessionMetaDir, e.Name()),
			ModTime: info.ModTime(),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})
	return files, nil
}

func parseISO(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05.000-07:00",
		"2006-01-02T15:04:05.000000-07:00",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp: %s", s)
}
