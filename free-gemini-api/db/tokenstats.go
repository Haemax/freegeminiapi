package db

import (
	"log"
	"sync"
)

// TokenUsage records token accounting for one request.
type TokenUsage struct {
	Model            string
	AccountTier      string
	SessionID        string
	PromptTokens     int
	HistoryTokens    int
	CompletionTokens int
	SavedTokens      int
	DedupedRepeats   int
	EvictedMessages  int
	Truncated        bool
}

// TokenStatsSummary aggregates recorded usage.
type TokenStatsSummary struct {
	Requests      int64
	PromptTokens  int64
	HistoryTokens int64
	SavedTokens   int64
	EvictedMsgs   int64
}

const tokenUsageTableSQL = `CREATE TABLE IF NOT EXISTS token_stats (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	model TEXT NOT NULL,
	account_tier TEXT,
	session_id TEXT,
	prompt_tokens INTEGER DEFAULT 0,
	history_tokens INTEGER DEFAULT 0,
	completion_tokens INTEGER DEFAULT 0,
	saved_tokens INTEGER DEFAULT 0,
	deduped_repeats INTEGER DEFAULT 0,
	evicted_messages INTEGER DEFAULT 0,
	truncated INTEGER DEFAULT 0,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`

const tokenInsertSQL = `INSERT INTO token_stats (model, account_tier, session_id, prompt_tokens, history_tokens, completion_tokens, saved_tokens, deduped_repeats, evicted_messages, truncated) VALUES (?,?,?,?,?,?,?,?,?,?)`

const tokenSelectSQL = `SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(history_tokens),0), COALESCE(SUM(saved_tokens),0), COALESCE(SUM(evicted_messages),0) FROM token_stats`

var tokenTableOnce sync.Once

// EnsureTokenStatsTable creates the token_stats table on first use.
func EnsureTokenStatsTable() {
	tokenTableOnce.Do(func() {
		if DB == nil {
			return
		}
		if _, err := DB.Exec(tokenUsageTableSQL); err != nil {
			log.Printf("[token-stats] create table failed: %v", err)
		}
	})
}

// RecordTokenUsage persists one request's token accounting.
// Best-effort: an accounting failure must never fail a chat request.
func RecordTokenUsage(u TokenUsage) {
	if DB == nil {
		return
	}
	EnsureTokenStatsTable()
	trunc := 0
	if u.Truncated {
		trunc = 1
	}
	_, err := DB.Exec(tokenInsertSQL,
		u.Model, u.AccountTier, u.SessionID, u.PromptTokens, u.HistoryTokens,
		u.CompletionTokens, u.SavedTokens, u.DedupedRepeats, u.EvictedMessages, trunc,
	)
	if err != nil {
		log.Printf("[token-stats] insert failed: %v", err)
	}
}

// GetTokenStatsSummary returns totals across all recorded requests.
func GetTokenStatsSummary() TokenStatsSummary {
	var s TokenStatsSummary
	if DB == nil {
		return s
	}
	EnsureTokenStatsTable()
	_ = DB.QueryRow(tokenSelectSQL).Scan(
		&s.Requests, &s.PromptTokens, &s.HistoryTokens, &s.SavedTokens, &s.EvictedMsgs,
	)
	return s
}
