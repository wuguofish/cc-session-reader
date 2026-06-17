package session

// TranscriptReader abstracts transcript parsing so consumers
// don't couple to a specific JSONL format.
type TranscriptReader interface {
	ReadAll(path string) ([]Event, error)
}
