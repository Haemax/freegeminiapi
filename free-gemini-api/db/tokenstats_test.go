package db

import (
	"path/filepath"
	"sync"
	"testing"
)

// freshTokenDB opens a throwaway SQLite file, sets the package DB var, and
// re-arms the one-time table creation so RecordTokenUsage must create the
// table itself during the test.
func freshTokenDB(t *testing.T) func() {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token.db")
	_, err := InitDBAt(p)
	if err != nil {
		t.Fatalf("InitDBAt: %v", err)
	}
	tokenTableOnce = sync.Once{}
	return func() {
		if DB != nil {
			_ = DB.Close()
			DB = nil
		}
	}
}

func TestRecordTokenUsageCreatesTableOnFirstUse(t *testing.T) {
	cleanup := freshTokenDB(t)
	defer cleanup()

	// No explicit EnsureTokenStatsTable — RecordTokenUsage must do it itself.
	RecordTokenUsage(TokenUsage{
		Model:            "gemini-2.0-flash",
		AccountTier:      "free",
		SessionID:        "sess-a",
		PromptTokens:     100,
		HistoryTokens:    250,
		CompletionTokens: 40,
		SavedTokens:      30,
		DedupedRepeats:   2,
		EvictedMessages:  1,
		Truncated:        true,
	})

	s := GetTokenStatsSummary()
	if s.Requests != 1 {
		t.Fatalf("want 1 request, got %d", s.Requests)
	}
	if s.PromptTokens != 100 {
		t.Fatalf("prompt tokens wrong: got %d", s.PromptTokens)
	}
	if s.HistoryTokens != 250 || s.SavedTokens != 30 || s.EvictedMsgs != 1 {
		t.Fatalf("context totals wrong: %+v", s)
	}
}

func TestRecordTokenUsageAccumulates(t *testing.T) {
	cleanup := freshTokenDB(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		RecordTokenUsage(TokenUsage{Model: "m", SessionID: "s", PromptTokens: 10})
	}

	s := GetTokenStatsSummary()
	if s.Requests != 3 {
		t.Fatalf("want 3 requests, got %d", s.Requests)
	}
	if s.PromptTokens != 30 {
		t.Fatalf("want 30 prompt tokens, got %d", s.PromptTokens)
	}
}

func TestTokenStatsSummaryOnEmptyDB(t *testing.T) {
	cleanup := freshTokenDB(t)
	defer cleanup()

	s := GetTokenStatsSummary()
	if s.Requests != 0 || s.PromptTokens != 0 || s.SavedTokens != 0 {
		t.Fatalf("want zero summary, got %+v", s)
	}
}
