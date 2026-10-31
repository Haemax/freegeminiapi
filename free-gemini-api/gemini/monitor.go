package gemini

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// cachedImageCookies holds pre-fetched cookies for image/video downloads.
// Use GetCachedImageCookies() / storeCachedImageCookies() -- do not access directly.
var cachedImageCookies atomic.Value // string

// GetCachedImageCookies returns the current cached cookie jar ("" if unset).
func GetCachedImageCookies() string {
	if v := cachedImageCookies.Load(); v != nil {
		return v.(string)
	}
	return ""
}

func storeCachedImageCookies(s string) { cachedImageCookies.Store(s) }

// OnCookiesUpdated is a callback triggered when new cookies are synced
var OnCookiesUpdated func()

// CookieHealthInfo represents the health and remaining lifetime of active cookies
type CookieHealthInfo struct {
	Status            string  `json:"status"` // "healthy", "expiring_soon", "expired", "no_cookies"
	MinTTLSeconds     float64 `json:"min_ttl_seconds"`
	MinTTLFormatted   string  `json:"min_ttl_formatted"`
	LastSyncedAt      string  `json:"last_synced_at"`
	ConnectedWorkers  int     `json:"connected_workers"`
	MonitoredAccounts int     `json:"monitored_accounts"`
}

var (
	activeClients     []*websocket.Conn
	activeClientsMu   sync.Mutex
	lastSyncMu        sync.Mutex
	lastSyncTimestamp time.Time
	lastBroadcastSync time.Time
)

// BroadcastCookieRefresh sends a trigger_sync message to all connected Chrome Extension clients
func BroadcastCookieRefresh() {
	activeClientsMu.Lock()
	defer activeClientsMu.Unlock()

	lastSyncMu.Lock()
	if time.Since(lastBroadcastSync) < 20*time.Second {
		lastSyncMu.Unlock()
		return // Coalesce rapid consecutive broadcasts
	}
	lastBroadcastSync = time.Now()
	lastSyncMu.Unlock()

	if len(activeClients) == 0 {
		log.Println("ℹ️ No Chrome Extension workers currently connected to receive trigger_sync")
		return
	}

	log.Printf("📢 Triggering memory sync to %d connected Chrome Extension worker(s)...", len(activeClients))

	payload := map[string]string{"type": "trigger_sync"}

	for i := len(activeClients) - 1; i >= 0; i-- {
		conn := activeClients[i]
		err := conn.WriteJSON(payload)
		if err != nil {
			log.Printf("⚠️ Failed to write to extension client: %v. Removing client.", err)
			conn.Close()
			activeClients = append(activeClients[:i], activeClients[i+1:]...)
		}
	}
}

// InspectAccountCookieHealth analyzes active account cookies and returns health status
func InspectAccountCookieHealth() CookieHealthInfo {
	accountFiles := GetAvailableAccountCookieFiles()
	info := CookieHealthInfo{
		Status:            "no_cookies",
		ConnectedWorkers:  GetActiveWorkerCount(),
		MonitoredAccounts: len(accountFiles),
	}

	lastSyncMu.Lock()
	if !lastSyncTimestamp.IsZero() {
		info.LastSyncedAt = lastSyncTimestamp.Format(time.RFC3339)
	}
	lastSyncMu.Unlock()

	if len(accountFiles) == 0 {
		return info
	}

	now := time.Now().Unix()
	minRemaining := float64(999999999)
	foundTargetCookie := false

	for _, fPath := range accountFiles {
		data, err := os.ReadFile(fPath)
		if err != nil {
			continue
		}
		var cookies []CookieObject
		if err := json.Unmarshal(data, &cookies); err != nil {
			continue
		}

		for _, ck := range cookies {
			if (ck.Name == "__Secure-1PSIDTS" || ck.Name == "__Secure-3PSIDTS" || ck.Name == "SIDCC") && ck.ExpirationDate > 1700000000 {
				rem := ck.ExpirationDate - float64(now)
				foundTargetCookie = true
				if rem < minRemaining {
					minRemaining = rem
				}
			}
		}
	}

	if !foundTargetCookie {
		info.Status = "healthy"
		info.MinTTLSeconds = 86400
		info.MinTTLFormatted = "> 24h"
		return info
	}

	info.MinTTLSeconds = minRemaining
	if minRemaining <= 0 {
		info.Status = "expired"
		info.MinTTLFormatted = "Expired"
	} else {
		hours := int(minRemaining) / 3600
		mins := (int(minRemaining) % 3600) / 60
		if hours > 0 {
			info.MinTTLFormatted = fmt.Sprintf("%dh %dm", hours, mins)
		} else {
			info.MinTTLFormatted = fmt.Sprintf("%dm", mins)
		}

		if minRemaining < 2*3600 {
			info.Status = "expiring_soon"
		} else {
			info.Status = "healthy"
		}
	}

	return info
}

// StartCookieWatchdog launches the passive background health monitor (on-demand sync mode)
func StartCookieWatchdog() {
	go func() {
		log.Println("🛡️ Passive Cookie Health Monitor started (logs status every 30m, on-demand sync only)")
		for {
			time.Sleep(30 * time.Minute)
			health := InspectAccountCookieHealth()
			log.Printf("📊 Passive Health Check: status=%s, remaining_ttl=%s, monitored_accounts=%d", health.Status, health.MinTTLFormatted, health.MonitoredAccounts)
			if health.Status == "expired" {
				log.Printf("🚨 Health Monitor: Cookie expired detected (%s). Requesting on-demand refresh...", health.MinTTLFormatted)
				BroadcastCookieRefresh()
			}

			// -- chrome-manage fast path (PRD Step 10 / Option A) ---------
			// The extension broadcast above only helps if a browser happens to
			// be open. This does not need one, and it also reacts to
			// expiring_soon rather than only expired. Rate-limited per profile
			// by shouldRequestRefresh (30 min), because expiring_soon is a
			// state and would otherwise fire on every tick.
			for _, acct := range InspectPerAccountCookieHealth() {
				if acct.Status != "expiring_soon" && acct.Status != "expired" {
					continue
				}
				if !acct.HasProfile {
					log.Printf("chrome-manage: account %s is %s (ttl=%s) but maps to no profile - skipped", acct.AccountID, acct.Status, acct.MinTTLFormatted)
					continue
				}
				if shouldRequestRefresh(acct.ProfileID) {
					log.Printf("chrome-manage: account %s is %s (ttl=%s) -> requesting refresh of profile %d", acct.AccountID, acct.Status, acct.MinTTLFormatted, acct.ProfileID)
					go callChromeManageRefresh(acct.ProfileID)
				}
			}
		}
	}()
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow Chrome Extension context
	},
}

// ExtensionMessage is the envelope sent by the Chrome extension over the WebSocket bridge.
//   • type=ping            -> server replies {"type":"pong"} (keepalive)
//   • type=cookies_payload -> server persists cookies; force_pro / tier drives tier override
type ExtensionMessage struct {
	Type      string         `json:"type"`
	Cookies   []CookieObject `json:"cookies,omitempty"`
	ForcePro  bool           `json:"force_pro,omitempty"`
	Tier      string         `json:"tier,omitempty"`
	UserToken string         `json:"user_token,omitempty"`
}

// GetActiveWorkerCount returns number of connected Chrome Extension workers
func GetActiveWorkerCount() int {
	activeClientsMu.Lock()
	defer activeClientsMu.Unlock()
	return len(activeClients)
}

var nonAlnumRe = regexp.MustCompile(`[^a-zA-Z0-9]`)

func getAccountIDFromCookies(cookies []CookieObject) string {
	for _, c := range cookies {
		if c.Name == "__Secure-1PSID" || c.Name == "SID" || c.Name == "HSID" {
			cleanVal := nonAlnumRe.ReplaceAllString(c.Value, "")
			if len(cleanVal) > 10 {
				return cleanVal[:10]
			}
			return cleanVal
		}
	}
	return "primary"
}

// GetAvailableAccountCookieFiles returns paths of all active account cookie files
func GetAvailableAccountCookieFiles() []string {
	files, err := filepath.Glob(filepath.Join("cookies", "account_*.json"))
	if err != nil || len(files) == 0 {
		defaultPath := filepath.Join("cookies", "cookies.json")
		if _, err := os.Stat(defaultPath); err == nil {
			return []string{defaultPath}
		}
		return nil
	}
	return files
}

// GetActiveAccountCount returns the number of distinct account profiles saved
func GetActiveAccountCount() int {
	return len(GetAvailableAccountCookieFiles())
}

// ---- cookie push hygiene -------------------------------------------------

// ErrNoLoginCookie is returned when a pushed cookie set has no Google login cookie
// (a logged-out Chrome profile). Such a set can never open a session.
var ErrNoLoginCookie = errors.New("cookies contain no login cookie")

var (
	cookieHashMu   sync.Mutex
	lastCookieHash = map[string]string{}

	reloadMu    sync.Mutex
	reloadTimer *time.Timer
)

func hasAuthCookies(cookies []CookieObject) bool {
	for _, c := range cookies {
		if (c.Name == "__Secure-1PSID" || c.Name == "SID") && c.Value != "" {
			return true
		}
	}
	return false
}

func cookieValue(cookies []CookieObject, name string) string {
	for _, c := range cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// volatileCookies are rewritten by Google on almost every request (SIDCC
// family, NID, COMPASS). They carry no login identity: a change in only these
// must NOT count as "new cookies", otherwise any open Google tab makes the
// extension re-push every few seconds, and each push rewrites the file, wipes
// the session cache and reloads the whole worker pool.
var volatileCookies = map[string]bool{
	"SIDCC":            true,
	"__Secure-1PSIDCC": true,
	"__Secure-3PSIDCC": true,
	"NID":              true,
	"COMPASS":          true,
}

func cookieSetHash(cookies []CookieObject) string {
	pairs := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if volatileCookies[c.Name] {
			continue
		}
		pairs = append(pairs, c.Name+"="+c.Value)
	}
	sort.Strings(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, ";")))
	return hex.EncodeToString(sum[:])
}

// isStaleSubset reports whether incoming is a smaller cookie set for the same login
// as the one already on disk (two Chrome connections alternating 17 / 15 cookies).
func isStaleSubset(path string, incoming []CookieObject) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var existing []CookieObject
	if json.Unmarshal(data, &existing) != nil {
		return false
	}
	psid := cookieValue(incoming, "__Secure-1PSID")
	return psid != "" && psid == cookieValue(existing, "__Secure-1PSID") && len(incoming) < len(existing)
}

// scheduleCookiesUpdated coalesces bursts of pushes into a single pool reload.
func scheduleCookiesUpdated() {
	if OnCookiesUpdated == nil {
		return
	}
	reloadMu.Lock()
	defer reloadMu.Unlock()
	if reloadTimer != nil {
		reloadTimer.Stop()
	}
	reloadTimer = time.AfterFunc(2*time.Second, func() {
		if cb := OnCookiesUpdated; cb != nil {
			cb()
		}
	})
}

// ProcessAndSaveCookies stores the received cookies into the account file, updates timestamps, and fires OnCookiesUpdated
//
// profileID, when non-nil and positive, selects the file: cookies/account_profile_<N>.json.
// That is the farm's own per-profile file, so a chrome-manage refresh UPDATES the account it
// already has instead of adding a second one for the same Google account.
//
// Without it the file is named from the cookie VALUE (getAccountIDFromCookies), which is not
// stable: __Secure-1PSID rotates, so nearly every refresh produced a hash matching no existing
// file and minted a brand-new account_<hash>.json. The pool globs account_*.json and loads each
// as its own worker, so a single Google account ended up served by several workers at once - and
// with max_concurrency_per_worker: 3 that multiplies the concurrent requests hitting that
// account, on a farm whose whole premise is avoiding Google lockouts.
// Measured 2026-09-27: 54 -> 55 workers after a single refresh.
//
// The Chrome extension has no profile id and keeps the old behaviour by passing nil.
func ProcessAndSaveCookies(cookies []CookieObject, profileID *int) (string, string, error) {
	accountID := getAccountIDFromCookies(cookies)
	if profileID != nil && *profileID > 0 {
		accountID = fmt.Sprintf("profile_%d", *profileID)
	}
	accountFilePath := filepath.Join("cookies", fmt.Sprintf("account_%s.json", accountID))

	if !hasAuthCookies(cookies) {
		log.Printf("⚠️ Ignoring %d cookies for [%s]: no login cookie (logged-out Chrome profile?)", len(cookies), accountID)
		return "", accountID, fmt.Errorf("%w (account %s)", ErrNoLoginCookie, accountID)
	}
	if isStaleSubset(accountFilePath, cookies) {
		return accountFilePath, accountID, nil
	}
	cookieHashMu.Lock()
	h := cookieSetHash(cookies)
	_, statErr := os.Stat(accountFilePath)
	unchanged := lastCookieHash[accountID] == h && statErr == nil
	lastCookieHash[accountID] = h
	cookieHashMu.Unlock()
	if unchanged {
		return accountFilePath, accountID, nil
	}

	log.Printf("🍪 Received %d cookies for Account [%s]", len(cookies), accountID)

	lastSyncMu.Lock()
	lastSyncTimestamp = time.Now()
	lastSyncMu.Unlock()

	data, err := json.MarshalIndent(cookies, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal cookies: %w", err)
	}

	os.MkdirAll("cookies", 0755)

	if err := os.WriteFile(accountFilePath, data, 0644); err != nil {
		return "", "", fmt.Errorf("failed to save %s: %w", accountFilePath, err)
	}
	log.Printf("💾 Stored account profile: %s", accountFilePath)

	// Refresh CachedImageCookies
	var parts []string
	for _, ck := range cookies {
		parts = append(parts, fmt.Sprintf("%s=%s", ck.Name, ck.Value))
	}
	storeCachedImageCookies(strings.Join(parts, "; "))

	// Trigger callback to reload active sessions
	scheduleCookiesUpdated()

	return accountFilePath, accountID, nil
}

// StartCookieWebSocketServer starts a local WebSocket server to receive cookies from the extension
func StartCookieWebSocketServer(port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("❌ WS Upgrade failed: %v", err)
			return
		}

		activeClientsMu.Lock()
		activeClients = append(activeClients, conn)
		count := len(activeClients)
		activeClientsMu.Unlock()

		log.Printf("🔌 Chrome Extension connected to cookie bridge (Active Workers: %d)", count)

		defer func() {
			conn.Close()
			activeClientsMu.Lock()
			for i, c := range activeClients {
				if c == conn {
					activeClients = append(activeClients[:i], activeClients[i+1:]...)
					break
				}
			}
			remCount := len(activeClients)
			activeClientsMu.Unlock()
			log.Printf("🔌 Chrome Extension disconnected (Active Workers: %d)", remCount)
		}()

		for {
			_, msgBytes, err := conn.ReadMessage()
			if err != nil {
				break
			}

			var msg ExtensionMessage
			if err := json.Unmarshal(msgBytes, &msg); err != nil {
				log.Printf("❌ Failed to decode extension message: %v", err)
				continue
			}

			if msg.Type == "ping" {
				conn.WriteJSON(map[string]string{"type": "pong"})
				continue
			}

			if msg.Type == "cookies_payload" && len(msg.Cookies) > 0 {
				accID, _, err := ProcessAndSaveCookies(msg.Cookies, nil)
				if err != nil {
					log.Printf("❌ Failed to process cookies: %v", err)
				} else if accID != "" {
					if msg.ForcePro || strings.EqualFold(msg.Tier, "subscription") {
						SetForcedPro(accID)
					} else if strings.EqualFold(msg.Tier, "free") {
						ClearForcedPro(accID)
					}
				}
			}

			if msg.Type == "deepseek_session_payload" {
				err := SaveDeepSeekSession(msg.UserToken, msg.Cookies)
				if err != nil {
					log.Printf("❌ Failed to process DeepSeek session: %v", err)
				}
			}
		}
	})

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	log.Printf("📡 Cookie WebSocket Server listening on %s", addr)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("⚠️ Cookie WebSocket Server warning: %v", err)
	}
}

func SaveDeepSeekSession(token string, cookies []CookieObject) error {
	data := map[string]interface{}{
		"user_token": token,
		"cookies":    cookies,
		"updated_at": time.Now().Unix(),
	}
	bytes, _ := json.MarshalIndent(data, "", "  ")
	path := filepath.Join("cookies", "deepseek_account.json")
	log.Printf("💾 Stored DeepSeek account profile: %s", path)
	return os.WriteFile(path, bytes, 0644)
}