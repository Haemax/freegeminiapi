package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const DBFileName = "data/gemini.db"

var (
	DB     *sql.DB
	dbMu   sync.Mutex
	dbPath = DBFileName
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	conversation_id TEXT,
	user_id TEXT,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	model TEXT,
	tokens INTEGER DEFAULT 0,
	account_tier TEXT DEFAULT 'free',
	account_id TEXT DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_user ON messages(user_id);
CREATE INDEX IF NOT EXISTS idx_messages_tier ON messages(account_tier);
CREATE INDEX IF NOT EXISTS idx_messages_model ON messages(model);
CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at);
CREATE INDEX IF NOT EXISTS idx_messages_conv_id ON messages(conversation_id, id DESC);

CREATE TABLE IF NOT EXISTS media_generations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	type TEXT NOT NULL,
	prompt TEXT NOT NULL,
	file_name TEXT NOT NULL,
	file_path TEXT NOT NULL,
	url TEXT,
	aspect_ratio TEXT,
	response_id TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_media_type ON media_generations(type);
CREATE INDEX IF NOT EXISTS idx_media_created ON media_generations(created_at);

CREATE TABLE IF NOT EXISTS request_logs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	endpoint TEXT NOT NULL,
	method TEXT NOT NULL,
	user_ip TEXT,
	status_code INTEGER,
	elapsed_ms REAL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_reqlogs_created ON request_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_reqlogs_status ON request_logs(status_code);

CREATE TABLE IF NOT EXISTS accounts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id TEXT UNIQUE NOT NULL,
	cookie_file TEXT NOT NULL,
	status TEXT DEFAULT 'active',
	tier TEXT DEFAULT 'free',
	total_requests INTEGER DEFAULT 0,
	last_used_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_accounts_status ON accounts(status);
`

var migrationSQL = []string{
	`ALTER TABLE messages ADD COLUMN account_tier TEXT DEFAULT 'free'`,
	`ALTER TABLE messages ADD COLUMN account_id TEXT DEFAULT ''`,
	`ALTER TABLE accounts ADD COLUMN tier TEXT DEFAULT 'free'`,
}

// InitDB opens the default DB (idempotent for the same path).
func InitDB() (*sql.DB, error) {
	return InitDBAt(dbPath)
}

// InitDBAt opens (or replaces) the DB at path. Tests pass a TempDir or
// "file::memory:?cache=shared"; production uses InitDB().
func InitDBAt(path string) (*sql.DB, error) {
	dbMu.Lock()
	defer dbMu.Unlock()

	if DB != nil {
		if dbPath == path {
			return DB, nil
		}
		_ = DB.Close()
		DB = nil
	}

	if !strings.HasPrefix(path, "file:") && path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, err
			}
		}
	}

	database, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=busy_timeout(5000)&_pragma=temp_store(memory)")
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(25)
	database.SetMaxIdleConns(10)
	database.SetConnMaxLifetime(time.Hour)

	// Execute high-concurrency pragmas directly on setup
	if _, err := database.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA cache_size = -64000;
		PRAGMA busy_timeout = 5000;
	`); err != nil {
		log.Printf("[WARN] Failed to set DB pragmas: %v", err)
	}

	if _, err := database.Exec(schemaSQL); err != nil {
		database.Close()
		return nil, err
	}
	for _, m := range migrationSQL {
		_, _ = database.Exec(m)
	}

	DB = database
	dbPath = path
	log.Printf("🗄️  SQLite Database initialized successfully at %s (WAL Mode)", path)
	return DB, nil
}

type LogEvent struct {
	Type           string
	ConversationID string
	UserID         string
	Role           string
	Content        string
	Model          string
	Tokens         int
	AccountTier    string
	AccountID      string
	Endpoint       string
	Method         string
	UserIP         string
	StatusCode     int
	ElapsedMs      float64
}

var (
	logQueue = make(chan LogEvent, 1000)
	logQueueClosed sync.Once
)

func init() {
	// Start 2 async database writer workers
	for i := 0; i < 2; i++ {
		go dbLogWorker()
	}
}

func dbLogWorker() {
	for event := range logQueue {
		switch event.Type {
		case "message":
			_ = LogMessageWithTier(event.ConversationID, event.UserID, event.Role, event.Content, event.Model, event.Tokens, event.AccountTier, event.AccountID)
		case "request":
			_ = LogRequest(event.Endpoint, event.Method, event.UserIP, event.StatusCode, event.ElapsedMs)
		}
	}
}

// ShutdownLogQueue gracefully stops the async log workers
func ShutdownLogQueue() {
	logQueueClosed.Do(func() {
		close(logQueue)
	})
}

func AsyncLogMessageWithTier(conversationID, userID, role, content, model string, tokens int, accountTier, accountID string) {
	select {
	case logQueue <- LogEvent{
		Type:           "message",
		ConversationID: conversationID,
		UserID:         userID,
		Role:           role,
		Content:        content,
		Model:          model,
		Tokens:         tokens,
		AccountTier:    accountTier,
		AccountID:      accountID,
	}:
	default:
		// Queue full fallback to synchronous write
		_ = LogMessageWithTier(conversationID, userID, role, content, model, tokens, accountTier, accountID)
	}
}

func AsyncLogRequest(endpoint, method, userIP string, statusCode int, elapsedMs float64) {
	select {
	case logQueue <- LogEvent{
		Type:       "request",
		Endpoint:   endpoint,
		Method:     method,
		UserIP:     userIP,
		StatusCode: statusCode,
		ElapsedMs:  elapsedMs,
	}:
	default:
		_ = LogRequest(endpoint, method, userIP, statusCode, elapsedMs)
	}
}
func LogMessage(conversationID, userID, role, content, model string, tokens int) error {
	return LogMessageWithTier(conversationID, userID, role, content, model, tokens, "free", "")
}

func LogMessageWithTier(conversationID, userID, role, content, model string, tokens int, accountTier, accountID string) error {
	if DB == nil {
		return nil
	}
	if accountTier == "" {
		accountTier = "free"
	}
	query := `INSERT INTO messages (conversation_id, user_id, role, content, model, tokens, account_tier, account_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := DB.Exec(query, conversationID, userID, role, content, model, tokens, accountTier, accountID)
	return err
}

func LogMediaGeneration(mediaType, prompt, fileName, filePath, url, aspectRatio, responseID string) error {
	if DB == nil {
		return nil
	}
	query := `INSERT INTO media_generations (type, prompt, file_name, file_path, url, aspect_ratio, response_id) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := DB.Exec(query, mediaType, prompt, fileName, filePath, url, aspectRatio, responseID)
	return err
}

func LogRequest(endpoint, method, userIP string, statusCode int, elapsedMs float64) error {
	if DB == nil {
		return nil
	}
	query := `INSERT INTO request_logs (endpoint, method, user_ip, status_code, elapsed_ms) VALUES (?, ?, ?, ?, ?)`
	_, err := DB.Exec(query, endpoint, method, userIP, statusCode, elapsedMs)
	return err
}

func GetRecentMessages(conversationID string, limit int) ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	rows, err := DB.Query(`SELECT role, content, model, created_at FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT ?`, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]any
	for rows.Next() {
		var role, content, model, createdAt string
		if err := rows.Scan(&role, &content, &model, &createdAt); err == nil {
			result = append(result, map[string]any{
				"role":       role,
				"content":    content,
				"model":      model,
				"created_at": createdAt,
			})
		}
	}
	return result, nil
}

func SearchMessages(query string, limit int) ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	searchPattern := "%" + query + "%"
	rows, err := DB.Query(`SELECT id, COALESCE(role, ''), COALESCE(content, ''), COALESCE(model, ''), COALESCE(created_at, '') FROM messages WHERE content LIKE ? ORDER BY id DESC LIMIT ?`, searchPattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]any
	for rows.Next() {
		var id int
		var role, content, model, createdAt string
		if err := rows.Scan(&id, &role, &content, &model, &createdAt); err == nil {
			result = append(result, map[string]any{
				"id":         id,
				"role":       role,
				"content":    content,
				"model":      model,
				"created_at": createdAt,
			})
		}
	}
	return result, nil
}

func GetMediaByPrompt(mediaType, query string, limit int) ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	sqlQuery := `SELECT id, type, prompt, file_name, url, created_at FROM media_generations WHERE 1=1`
	var args []any
	if mediaType != "" && mediaType != "all" {
		sqlQuery += ` AND type = ?`
		args = append(args, mediaType)
	}
	if query != "" {
		sqlQuery += ` AND prompt LIKE ?`
		args = append(args, "%"+query+"%")
	}
	sqlQuery += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := DB.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]any
	for rows.Next() {
		var id int
		var mType, prompt, fileName, urlStr, createdAt string
		if err := rows.Scan(&id, &mType, &prompt, &fileName, &urlStr, &createdAt); err == nil {
			result = append(result, map[string]any{
				"id":         id,
				"type":       mType,
				"prompt":     prompt,
				"file_name":  fileName,
				"url":        urlStr,
				"created_at": createdAt,
			})
		}
	}
	return result, nil
}

// GetSystemStats — batched to 6 queries total (was 11).
func GetSystemStats() (map[string]any, error) {
	if DB == nil {
		return map[string]any{"status": "database_not_connected"}, nil
	}
	var totalMessages, totalMedia, totalRequests, totalTokens int
	var totalImages, totalVideos, totalMusic int
	var freeMessages, freeTokens, subMessages, subTokens int

	_ = DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(tokens), 0) FROM messages`).
		Scan(&totalMessages, &totalTokens)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM request_logs`).Scan(&totalRequests)

	if rows, err := DB.Query(`SELECT type, COUNT(*) FROM media_generations GROUP BY type`); err == nil {
		for rows.Next() {
			var t string
			var n int
			if rows.Scan(&t, &n) == nil {
				switch t {
				case "image":
					totalImages = n
				case "video":
					totalVideos = n
				case "music":
					totalMusic = n
				}
				totalMedia += n
			}
		}
		rows.Close()
	}

	_ = DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(tokens), 0) FROM messages WHERE COALESCE(account_tier, 'free') IN ('free', '')`).
		Scan(&freeMessages, &freeTokens)
	_ = DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(tokens), 0) FROM messages WHERE account_tier = 'subscription'`).
		Scan(&subMessages, &subTokens)

	modelStats := make(map[string]map[string]int)
	if mRows, err := DB.Query(`SELECT COALESCE(model, 'unknown'), COALESCE(account_tier, 'free'), COUNT(*), COALESCE(SUM(tokens), 0) FROM messages GROUP BY model, account_tier`); err == nil {
		for mRows.Next() {
			var mName, mTier string
			var mCount, mTok int
			if mRows.Scan(&mName, &mTier, &mCount, &mTok) == nil {
				key := fmt.Sprintf("%s (%s)", mName, mTier)
				modelStats[key] = map[string]int{"messages": mCount, "tokens": mTok}
			}
		}
		mRows.Close()
	}

	freeCostTokensUSD := float64(freeTokens) * 0.0000005
	subCostTokensUSD := float64(subTokens) * 0.0000025
	mediaCostUSD := (float64(totalImages) * 0.030) + (float64(totalVideos) * 1.200) + (float64(totalMusic) * 0.080)

	costSavedUSD := freeCostTokensUSD + subCostTokensUSD + mediaCostUSD
	usdToINR := GetUSDToINRRate()
	costSavedINR := costSavedUSD * usdToINR

	return map[string]any{
		"total_messages": totalMessages,
		"total_tokens":   totalTokens,
		"tier_breakdown": map[string]any{
			"free": map[string]any{
				"messages":       freeMessages,
				"tokens":         freeTokens,
				"cost_saved_usd": fmt.Sprintf("$%.4f", freeCostTokensUSD),
			},
			"subscription": map[string]any{
				"messages":       subMessages,
				"tokens":         subTokens,
				"cost_saved_usd": fmt.Sprintf("$%.4f", subCostTokensUSD),
			},
		},
		"model_breakdown":          modelStats,
		"total_media":              totalMedia,
		"total_images":             totalImages,
		"total_videos":             totalVideos,
		"total_music":              totalMusic,
		"total_requests":           totalRequests,
		"cost_saved_usd":           costSavedUSD,
		"cost_saved_inr":           costSavedINR,
		"cost_saved_usd_formatted": fmt.Sprintf("$%.4f", costSavedUSD),
		"cost_saved_inr_formatted": fmt.Sprintf("₹%.2f", costSavedINR),
		"engine":                   "SQLite WAL Mode",
		"database_file":            DBFileName,
	}, nil
}

func RecordAccountUsage(accountID, cookieFile, status, tier string) error {
	if DB == nil {
		return nil
	}
	if tier == "" {
		tier = "free"
	}
	query := `
	INSERT INTO accounts (account_id, cookie_file, status, tier, total_requests, last_used_at)
	VALUES (?, ?, ?, ?, 1, CURRENT_TIMESTAMP)
	ON CONFLICT(account_id) DO UPDATE SET
		cookie_file = excluded.cookie_file,
		status = excluded.status,
		tier = excluded.tier,
		total_requests = accounts.total_requests + 1,
		last_used_at = CURRENT_TIMESTAMP;
	`
	_, err := DB.Exec(query, accountID, cookieFile, status, tier)
	return err
}

func GetAccounts() ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	rows, err := DB.Query(`SELECT account_id, cookie_file, status, COALESCE(tier, 'free'), total_requests, last_used_at FROM accounts ORDER BY last_used_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []map[string]any
	for rows.Next() {
		var accountID, cookieFile, status, tier, lastUsed string
		var totalRequests int
		if err := rows.Scan(&accountID, &cookieFile, &status, &tier, &totalRequests, &lastUsed); err == nil {
			accounts = append(accounts, map[string]any{
				"account_id":     accountID,
				"cookie_file":    cookieFile,
				"status":         status,
				"tier":           tier,
				"total_requests": totalRequests,
				"last_used_at":   lastUsed,
			})
		}
	}
	return accounts, nil
}

func GetAllMessagesForExport() ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	query := `SELECT id, conversation_id, user_id, role, content, model, tokens, COALESCE(account_tier, 'free'), COALESCE(account_id, ''), created_at FROM messages ORDER BY id ASC LIMIT 5000`
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []map[string]any
	for rows.Next() {
		var id, tokens int
		var convID, userID, role, content, model, tier, accID, createdAt string
		if err := rows.Scan(&id, &convID, &userID, &role, &content, &model, &tokens, &tier, &accID, &createdAt); err == nil {
			list = append(list, map[string]any{
				"id": id, "conversation_id": convID, "user_id": userID,
				"role": role, "content": content, "model": model,
				"tokens": tokens, "account_tier": tier, "account_id": accID,
				"created_at": createdAt,
			})
		}
	}
	return list, nil
}

func GetAllMediaForExport() ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	query := `SELECT id, type, prompt, file_name, file_path, url, aspect_ratio, response_id, created_at FROM media_generations ORDER BY id DESC LIMIT 5000`
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []map[string]any
	for rows.Next() {
		var id int
		var mType, prompt, fileName, filePath, urlStr, aspect, respID, createdAt string
		if err := rows.Scan(&id, &mType, &prompt, &fileName, &filePath, &urlStr, &aspect, &respID, &createdAt); err == nil {
			list = append(list, map[string]any{
				"id": id, "type": mType, "prompt": prompt, "file_name": fileName,
				"file_path": filePath, "url": urlStr, "aspect_ratio": aspect,
				"response_id": respID, "created_at": createdAt,
			})
		}
	}
	return list, nil
}

func GetAllRequestLogsForExport() ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	query := `SELECT id, endpoint, method, user_ip, status_code, elapsed_ms, created_at FROM request_logs ORDER BY id DESC LIMIT 5000`
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []map[string]any
	for rows.Next() {
		var id, status int
		var endpoint, method, userIP, createdAt string
		var elapsed float64
		if err := rows.Scan(&id, &endpoint, &method, &userIP, &status, &elapsed, &createdAt); err == nil {
			list = append(list, map[string]any{
				"id": id, "endpoint": endpoint, "method": method, "user_ip": userIP,
				"status_code": status, "elapsed_ms": elapsed, "created_at": createdAt,
			})
		}
	}
	return list, nil
}

func GetUSDToINRRate() float64 {
	rate := 95.89
	if envRate := os.Getenv("USD_TO_INR"); envRate != "" {
		if r, err := strconv.ParseFloat(envRate, 64); err == nil && r > 0 {
			rate = r
		}
	}
	return rate
}