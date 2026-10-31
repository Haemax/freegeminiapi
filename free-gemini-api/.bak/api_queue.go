package api

import (
	"context"
	"errors"
	"fmt"
	"goapi/db"
	"goapi/gemini"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AccountWorker represents an individual Google Account worker in the pool
type AccountWorker struct {
	ID            string               `json:"id"`
	CookiePath    string               `json:"cookie_path"`
	Client        *gemini.GeminiClient `json:"-"`
	Tier          string               `json:"tier"` // "free" or "subscription"
	InFlight      int64                `json:"in_flight"`
	TotalServed   int64                `json:"total_served"`
	TotalErrors   int64                `json:"total_errors"`
	Status        string               `json:"status"` // "active", "busy", "cooldown", "recovering"
	CooldownUntil time.Time            `json:"cooldown_until"`
	AuthFailedAt  time.Time            `json:"-"` // when the worker last hit a login/session error
	LastUsed      time.Time            `json:"last_used"`
	mu            sync.Mutex
}
type affinityEntry struct {
	id string
	at time.Time
}

const affinityTTL = 1 * time.Hour

// StartAffinityCleaner periodically purges expired session affinity entries
func (p *WorkerPool) StartAffinityCleaner(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		for range ticker.C {
			now := time.Now()
			p.affinityMap.Range(func(key, value interface{}) bool {
				if entry, ok := value.(affinityEntry); ok {
					if now.Sub(entry.at) > affinityTTL {
						p.affinityMap.Delete(key)
					}
				}
				return true
			})
		}
	}()
}



// IsHealthy returns true if the worker is not in cooldown and is ready to accept requests
func (w *AccountWorker) IsHealthy() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.CooldownUntil.IsZero() && time.Now().Before(w.CooldownUntil) {
		return false
	}
	if w.Status == "cooldown" || w.Status == "recovering" {
		w.Status = "active"
		w.CooldownUntil = time.Time{}
	}
	return w.Client != nil
}

// WorkerPool manages the pool of Google account workers, load-balancing, and session affinity
type WorkerPool struct {
	mu                      sync.RWMutex
	workers                 []*AccountWorker
	affinityMap             sync.Map // sessionKey / convID / agentID -> AccountID
	rrIndex                 uint64   // atomic round-robin counter
	maxConcurrencyPerWorker int64    // max parallel requests per Google account (default: 3)
}

var (
	GlobalWorkerPool *WorkerPool
	poolOnce         sync.Once
)

// GetWorkerPool returns the singleton WorkerPool instance, initializing if needed
func GetWorkerPool() *WorkerPool {
	poolOnce.Do(func() {
		GlobalWorkerPool = &WorkerPool{
			maxConcurrencyPerWorker: 20,
		}
		GlobalWorkerPool.ReloadPool()
	})
	return GlobalWorkerPool
}

// InitWorkerPool initializes the global worker pool at server startup
func InitWorkerPool() {
	_ = GetWorkerPool()
}

// ReloadPool scans cookies directory, boots up new workers, and reloads active sessions
func (p *WorkerPool) ReloadPool() {
	p.mu.Lock()
	defer p.mu.Unlock()

	accountFiles := gemini.GetAvailableAccountCookieFiles()
	if len(accountFiles) == 0 {
		accountFiles = []string{CookiesFile}
	}

	existingMap := make(map[string]*AccountWorker)
	for _, w := range p.workers {
		existingMap[w.CookiePath] = w
	}

	var updatedWorkers []*AccountWorker

	for _, cFile := range accountFiles {
		accID := filepath.Base(cFile)
		accID = strings.TrimPrefix(accID, "account_")
		accID = strings.TrimSuffix(accID, ".json")

		if existing, ok := existingMap[cFile]; ok {
			// Existing worker — reload session
			// If the cookie file was rewritten after this worker's last auth failure,
			// the failure is stale: give the worker another chance.
			existing.mu.Lock()
			if !existing.AuthFailedAt.IsZero() {
				if fi, err := os.Stat(cFile); err == nil && fi.ModTime().After(existing.AuthFailedAt) {
					existing.AuthFailedAt = time.Time{}
					existing.CooldownUntil = time.Time{}
					existing.Status = "active"
				}
			}
			existing.mu.Unlock()
			if existing.Client != nil {
				_ = existing.Client.ReloadSession()
				workerTier := existing.Client.AccountTier
				if workerTier == "" {
					workerTier = "subscription"
				}
				existing.Tier = workerTier
			}
			updatedWorkers = append(updatedWorkers, existing)
			continue
		}

		// Check if file exists before creating client
		if _, err := os.Stat(cFile); os.IsNotExist(err) {
			continue
		}

		client, err := gemini.NewClient(cFile)
		if err != nil {
			log.Printf("⚠️ [WorkerPool] Could not initialize client for %s: %v", cFile, err)
			continue
		}
		_ = client.InitSession()

		workerTier := client.AccountTier
		if workerTier == "" {
			workerTier = "subscription"
		}
		worker := &AccountWorker{
			ID:          accID,
			CookiePath:  cFile,
			Client:      client,
			Tier:        workerTier,
			Status:      "active",
			LastUsed:    time.Now(),
		}
		updatedWorkers = append(updatedWorkers, worker)
		_ = db.RecordAccountUsage(accID, cFile, "active", client.AccountTier)
		log.Printf("🚀 [WorkerPool] Registered new Account Worker [%s] (Tier: %s) from %s", accID, worker.Tier, cFile)
	}

	// Ensure a Guest Worker is available for zero-login free requests or as fallback
	hasGuest := false
	for _, w := range updatedWorkers {
		if w.ID == "guest" {
			hasGuest = true
			break
		}
	}
	if !hasGuest {
		guestClient, err := gemini.NewClient("guest")
		if err == nil {
			_ = guestClient.InitSession()
			guestWorker := &AccountWorker{
				ID:         "guest",
				CookiePath: "guest",
				Client:     guestClient,
				Tier:       "free",
				Status:     "active",
				LastUsed:   time.Now(),
			}
			updatedWorkers = append(updatedWorkers, guestWorker)
			_ = db.RecordAccountUsage("guest", "guest", "active", "free")
			log.Printf("🌐 [WorkerPool] Registered Guest Worker [guest] (Tier: free) — Ready out-of-the-box without login!")
		}
	}

	p.workers = updatedWorkers
	log.Printf("✨ [WorkerPool] Pool reloaded: %d active account worker(s) available", len(p.workers))
}

// BindAffinity binds a conversation ID or agent key to an account ID
func (p *WorkerPool) BindAffinity(key, accountID string) {
	if key != "" && accountID != "" {
		p.affinityMap.Store(key, accountID)
	}
}

// AcquireWorker selects the best available worker (legacy wrapper defaulting to free/any tier)
func (p *WorkerPool) AcquireWorker(ctx context.Context, affinityKey string) (*AccountWorker, error) {
	return p.AcquireWorkerWithTier(ctx, affinityKey, "")
}

// AcquireWorkerWithTier selects the best available worker using Sticky Affinity, Tier Matching, or Least-Busy routing
func (p *WorkerPool) AcquireWorkerWithTier(ctx context.Context, affinityKey string, requiredTier string) (*AccountWorker, error) {
	return p.acquireWorkerExcluding(ctx, affinityKey, requiredTier, nil)
}

// acquireWorkerExcluding is AcquireWorkerWithTier that skips the worker IDs in exclude
// (workers already tried and failed for this request).
func (p *WorkerPool) acquireWorkerExcluding(ctx context.Context, affinityKey string, requiredTier string, exclude map[string]bool) (*AccountWorker, error) {
	timeout := 45 * time.Second
	deadline := time.Now().Add(timeout)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for available worker in pool")
		}

		p.mu.RLock()
		workersCount := len(p.workers)
		if workersCount == 0 {
			p.mu.RUnlock()
			// Attempt reload if empty
			p.ReloadPool()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// If subscription is strictly required, ensure at least one subscription or authenticated worker exists in pool
		if requiredTier == "subscription" {
			hasSubWorker := false
			for _, w := range p.workers {
				if w.Tier == "subscription" {
					hasSubWorker = true
					break
				}
			}
			if !hasSubWorker {
				for _, w := range p.workers {
					if w.ID != "guest" && w.Tier != "free" {
						hasSubWorker = true
						break
					}
				}
			}
			if !hasSubWorker {
				p.mu.RUnlock()
				return nil, fmt.Errorf("requested model requires a subscription (Gemini Advanced) account, but no subscription accounts are connected in the pool")
			}
		}

		// Bail out early if every worker has already been tried for this request
		if len(exclude) > 0 {
			remaining := 0
			for _, w := range p.workers {
				if !exclude[w.ID] {
					remaining++
				}
			}
			if remaining == 0 {
				p.mu.RUnlock()
				return nil, fmt.Errorf("no remaining workers to fail over to")
			}
		}

		// Check if ALL eligible workers are currently in cooldown / rate-limited
		hasCandidate := false
		allInCooldown := true
		minCooldownRemaining := 24 * time.Hour

		for _, w := range p.workers {
			if requiredTier == "subscription" && w.Tier != "subscription" && w.ID == "guest" {
				continue
			}
			if exclude[w.ID] {
				continue
			}
			hasCandidate = true
			w.mu.Lock()
			if w.CooldownUntil.IsZero() || time.Now().After(w.CooldownUntil) {
				allInCooldown = false
			} else {
				rem := time.Until(w.CooldownUntil)
				if rem < minCooldownRemaining {
					minCooldownRemaining = rem
				}
			}
			w.mu.Unlock()
		}

		if hasCandidate && allInCooldown {
			p.mu.RUnlock()
			if minCooldownRemaining <= 3*time.Second {
				time.Sleep(minCooldownRemaining)
				continue
			}
			secs := int(minCooldownRemaining.Seconds()) + 1
			tierName := "free"
			if requiredTier == "subscription" {
				tierName = "subscription"
			}
			return nil, fmt.Errorf("rate_limit_exceeded: all %s tier Google accounts reached rate limit / quota. Cooldown active, next worker ready in %ds. Connect more accounts to increase quota", tierName, secs)
		}

		// 1. Check Sticky Affinity (if worker matches required tier)
		if affinityKey != "" {
			if targetID, ok := p.affinityMap.Load(affinityKey); ok {
				for _, w := range p.workers {
					if w.ID == targetID && !exclude[w.ID] && w.IsHealthy() && atomic.LoadInt64(&w.InFlight) < p.maxConcurrencyPerWorker {
						// Ensure tier compatibility
						if requiredTier != "subscription" || w.Tier == "subscription" || w.ID != "guest" {
							atomic.AddInt64(&w.InFlight, 1)
							w.mu.Lock()
							w.LastUsed = time.Now()
							w.mu.Unlock()
							p.mu.RUnlock()
							return w, nil
						}
					}
				}
			}
		}

		// 2. Least-Busy Load Balancing
		var bestWorker *AccountWorker
		minInFlight := int64(999999)
		offset := atomic.AddUint64(&p.rrIndex, 1) % uint64(workersCount)

		if requiredTier == "subscription" {
			// Subscription requests strictly require subscription workers if present in pool, else any authenticated worker
			hasExplicitSub := false
			for _, w := range p.workers {
				if w.Tier == "subscription" && w.ID != "guest" {
					hasExplicitSub = true
					break
				}
			}

			for i := 0; i < workersCount; i++ {
				idx := (int(offset) + i) % workersCount
				w := p.workers[idx]
				if exclude[w.ID] || !w.IsHealthy() || w.ID == "guest" {
					continue
				}
				if hasExplicitSub && w.Tier != "subscription" {
					continue
				}
				inFlight := atomic.LoadInt64(&w.InFlight)
				if inFlight < p.maxConcurrencyPerWorker && inFlight < minInFlight {
					minInFlight = inFlight
					bestWorker = w
				}
			}
		} else {
			// Free requests:
			// Pass 1: Prefer free authenticated workers
			for i := 0; i < workersCount; i++ {
				idx := (int(offset) + i) % workersCount
				w := p.workers[idx]
				if exclude[w.ID] || !w.IsHealthy() || w.Tier != "free" || w.ID == "guest" {
					continue
				}
				inFlight := atomic.LoadInt64(&w.InFlight)
				if inFlight < p.maxConcurrencyPerWorker && inFlight < minInFlight {
					minInFlight = inFlight
					bestWorker = w
				}
			}

			// Pass 2: If free authenticated workers busy or absent, allow healthy subscription workers
			if bestWorker == nil {
				for i := 0; i < workersCount; i++ {
					idx := (int(offset) + i) % workersCount
					w := p.workers[idx]
					if exclude[w.ID] || !w.IsHealthy() || w.ID == "guest" {
						continue
					}
					inFlight := atomic.LoadInt64(&w.InFlight)
					if inFlight < p.maxConcurrencyPerWorker && inFlight < minInFlight {
						minInFlight = inFlight
						bestWorker = w
					}
				}
			}

			// Pass 3: Fall back to guest worker if no authenticated workers available or all on cooldown
			if bestWorker == nil {
				for i := 0; i < workersCount; i++ {
					idx := (int(offset) + i) % workersCount
					w := p.workers[idx]
					if exclude[w.ID] || !w.IsHealthy() {
						continue
					}
					inFlight := atomic.LoadInt64(&w.InFlight)
					if inFlight < p.maxConcurrencyPerWorker && inFlight < minInFlight {
						minInFlight = inFlight
						bestWorker = w
					}
				}
			}
		}

		if bestWorker != nil {
			atomic.AddInt64(&bestWorker.InFlight, 1)
			bestWorker.mu.Lock()
			bestWorker.LastUsed = time.Now()
			bestWorker.mu.Unlock()

			if affinityKey != "" {
				p.affinityMap.Store(affinityKey, bestWorker.ID)
			}
			p.mu.RUnlock()
			return bestWorker, nil
		}

		p.mu.RUnlock()
		// All workers at max concurrency or in cooldown — wait briefly
		time.Sleep(100 * time.Millisecond)
	}
}

// ReleaseWorker releases a worker, updates metrics, and triggers cooldown on 429/auth errors
func (p *WorkerPool) ReleaseWorker(worker *AccountWorker, err error) {
	if worker == nil {
		return
	}
	atomic.AddInt64(&worker.InFlight, -1)

	var newStatus, id, path, tier string
	var broadcast bool

	worker.mu.Lock()
	id, path, tier = worker.ID, worker.CookiePath, worker.Tier
	if err == nil {
		atomic.AddInt64(&worker.TotalServed, 1)
		worker.Status = "active"
		newStatus = "active"
	} else {
		atomic.AddInt64(&worker.TotalErrors, 1)
		s := strings.ToLower(err.Error())
		isRateLimit := strings.Contains(s, "429") ||
			strings.Contains(s, "rate limit") ||
			strings.Contains(s, "quota") ||
			strings.Contains(s, "resource_exhausted")
		isAuthOrExpiry := strings.Contains(s, "expired") ||
			strings.Contains(s, "servicelogin") ||
			strings.Contains(s, "snlm0e") ||
			strings.Contains(s, "401") ||
			strings.Contains(s, "unauthorized")

		switch {
		case isRateLimit:
			worker.Status = "cooldown"
			worker.CooldownUntil = time.Now().Add(60 * time.Second)
			newStatus = "rate_limited"
			log.Printf("⏳ [WorkerPool] Worker [%s] (Tier: %s) rate limited. Cooldown 60s.", id, tier)
		case isAuthOrExpiry:
			worker.Status = "recovering"
			worker.AuthFailedAt = time.Now()
			worker.CooldownUntil = time.Now().Add(10 * time.Minute)
			newStatus = "error"
			broadcast = true
			log.Printf("🚨 [WorkerPool] Worker [%s] auth/session issue. Triggering cookie sync...", id)
		default:
			newStatus = "error"
		}
	}
	worker.mu.Unlock()

	_ = db.RecordAccountUsage(id, path, newStatus, tier)
	if broadcast {
		go gemini.BroadcastCookieRefresh()
	}
}


func (p *WorkerPool) ExecuteQueued(ctx context.Context, affinityKey string, op func(*gemini.GeminiClient) (*gemini.GeminiResponse, error)) (*gemini.GeminiResponse, error) {
	return p.ExecuteQueuedWithTier(ctx, affinityKey, "", func(w *AccountWorker) (*gemini.GeminiResponse, error) {
		return op(w.Client)
	})
}

// ExecuteQueuedWithTier handles request execution with specific tier requirement
func (p *WorkerPool) ExecuteQueuedWithTier(ctx context.Context, affinityKey string, requiredTier string, op func(w *AccountWorker) (*gemini.GeminiResponse, error)) (*gemini.GeminiResponse, error) {
	maxAttempts := 3
	p.mu.RLock()
	availableCount := len(p.workers)
	p.mu.RUnlock()

	if availableCount > 0 && availableCount < maxAttempts {
		maxAttempts = availableCount
	}

	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		worker, err := p.AcquireWorkerWithTier(ctx, affinityKey, requiredTier)
		if err != nil {
			return nil, fmt.Errorf("failed to acquire worker from pool: %w", err)
		}

		resp, opErr := op(worker)
		p.ReleaseWorker(worker, opErr)

		if opErr == nil {
			if resp != nil && resp.ConversationID != "" {
				p.BindAffinity(resp.ConversationID, worker.ID)
				if affinityKey != "" {
					p.BindAffinity(affinityKey, worker.ID)
				}
			}
			return resp, nil
		}

		lastErr = opErr
		log.Printf("⚠️ [WorkerPool] Worker [%s] (Tier: %s) attempt %d/%d failed: %v. Retrying on another worker...", worker.ID, worker.Tier, attempt, maxAttempts, opErr)

		// On failure, remove affinity to avoid sticking to a broken worker
		if affinityKey != "" {
			p.affinityMap.Delete(affinityKey)
		}

		time.Sleep(200 * time.Millisecond)
	}

	return nil, lastErr
}

// ExecuteQueuedStream handles streaming completions through the worker queue with guaranteed release
func (p *WorkerPool) ExecuteQueuedStream(ctx context.Context, affinityKey string, op func(*gemini.GeminiClient) error) error {
	return p.ExecuteQueuedStreamWithTier(ctx, affinityKey, "", func(w *AccountWorker) error {
		return op(w.Client)
	})
}

// StreamStartedError marks a stream error that happened after output was already sent
// to the client. The pool must not retry those on another worker (the client would
// get duplicated or mixed output).
type StreamStartedError struct{ Err error }

func (e *StreamStartedError) Error() string { return e.Err.Error() }
func (e *StreamStartedError) Unwrap() error { return e.Err }

// isFailoverError reports whether err is worth retrying on a different worker
// (login/session problems and rate limits).
func isFailoverError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{"429", "rate limit", "quota", "resource_exhausted",
		"expired", "servicelogin", "redirected to login", "snlm0e", "401", "unauthorized"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// ExecuteQueuedStreamWithTier handles streaming completions requiring a specific account tier.
// If a worker fails with an auth/rate-limit error before any output was sent (op must wrap
// later errors in *StreamStartedError), the request is retried on another worker.
func (p *WorkerPool) ExecuteQueuedStreamWithTier(ctx context.Context, affinityKey string, requiredTier string, op func(w *AccountWorker) error) error {
	const maxAttempts = 3
	tried := map[string]bool{}
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		worker, err := p.acquireWorkerExcluding(ctx, affinityKey, requiredTier, tried)
		if err != nil {
			if lastErr != nil {
				// Report the real failure, not "no remaining workers"
				return lastErr
			}
			return fmt.Errorf("failed to acquire worker for streaming: %w", err)
		}

		streamErr := op(worker)
		p.ReleaseWorker(worker, streamErr)

		if streamErr == nil {
			if worker.Client != nil && worker.Client.ConversationID != "" {
				p.BindAffinity(worker.Client.ConversationID, worker.ID)
				if affinityKey != "" {
					p.BindAffinity(affinityKey, worker.ID)
				}
			}
			return nil
		}

		lastErr = streamErr
		// Never stick to a worker that just failed
		if affinityKey != "" {
			p.affinityMap.Delete(affinityKey)
		}

		var started *StreamStartedError
		if errors.As(streamErr, &started) || !isFailoverError(streamErr) || ctx.Err() != nil {
			return streamErr
		}

		tried[worker.ID] = true
		log.Printf("⚠️ [WorkerPool] Stream on worker [%s] failed before output (attempt %d/%d): %v. Failing over...", worker.ID, attempt, maxAttempts, streamErr)
	}
	return lastErr
}

// WorkerPoolStats represents the live metrics of the worker pool
type WorkerPoolStats struct {
	TotalWorkers        int           `json:"total_workers"`
	ActiveWorkers       int           `json:"active_workers"`
	FreeWorkers         int           `json:"free_workers"`
	SubscriptionWorkers int           `json:"subscription_workers"`
	TotalInFlight       int64         `json:"total_in_flight"`
	MaxConcurrency      int64         `json:"max_concurrency_per_worker"`
	Workers             []WorkerStats `json:"workers"`
}

type WorkerStats struct {
	ID                    string `json:"id"`
	CookiePath            string `json:"cookie_path"`
	Status                string `json:"status"`
	Tier                  string `json:"tier"`
	InFlight              int64  `json:"in_flight"`
	TotalServed           int64  `json:"total_served"`
	TotalErrors           int64  `json:"total_errors"`
	CooldownSecsRemaining int    `json:"cooldown_seconds_remaining"`
	LastUsed              string `json:"last_used"`
}

// GetStats returns real-time snapshot of the worker pool
func (p *WorkerPool) GetStats() WorkerPoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	stats := WorkerPoolStats{
		TotalWorkers:   len(p.workers),
		MaxConcurrency: p.maxConcurrencyPerWorker,
		Workers:        make([]WorkerStats, 0, len(p.workers)),
	}

	for _, w := range p.workers {
		inFlight := atomic.LoadInt64(&w.InFlight)
		stats.TotalInFlight += inFlight

		w.mu.Lock()
		cooldownRemaining := 0
		if !w.CooldownUntil.IsZero() && time.Now().Before(w.CooldownUntil) {
			cooldownRemaining = int(time.Until(w.CooldownUntil).Seconds())
		}
		isHealthy := w.Status == "active" && cooldownRemaining == 0
		if isHealthy {
			stats.ActiveWorkers++
		}

		if w.Tier == "subscription" {
			stats.SubscriptionWorkers++
		} else {
			stats.FreeWorkers++
		}

		ws := WorkerStats{
			ID:                    w.ID,
			CookiePath:            w.CookiePath,
			Status:                w.Status,
			Tier:                  w.Tier,
			InFlight:              inFlight,
			TotalServed:           atomic.LoadInt64(&w.TotalServed),
			TotalErrors:           atomic.LoadInt64(&w.TotalErrors),
			CooldownSecsRemaining: cooldownRemaining,
			LastUsed:              w.LastUsed.Format(time.RFC3339),
		}
		w.mu.Unlock()
		stats.Workers = append(stats.Workers, ws)
	}

	return stats
}

// DiscoverModelsFromWorkers triggers autonomous model and tier discovery on all connected accounts
func (p *WorkerPool) gcAffinity(olderThan time.Duration) {
	cutoff := time.Now().Add(-olderThan)
	p.affinityMap.Range(func(k, v any) bool {
		if e, ok := v.(affinityEntry); ok && e.at.Before(cutoff) {
			p.affinityMap.Delete(k)
		}
		return true
	})
}


func (p *WorkerPool) DiscoverModelsFromWorkers() (map[string]any, error) {
	p.mu.RLock()
	workersList := make([]*AccountWorker, len(p.workers))
	copy(workersList, p.workers)
	p.mu.RUnlock()

	discoveredCount := 0
	workerResults := make([]map[string]any, 0)

	for _, w := range workersList {
		if w.Client == nil {
			continue
		}
		newModels, err := gemini.DiscoverAccountModels(w.Client)
		if err == nil {
			discoveredCount += len(newModels)
			w.mu.Lock()
			w.Tier = w.Client.AccountTier
			w.mu.Unlock()
			_ = db.RecordAccountUsage(w.ID, w.CookiePath, w.Status, w.Client.AccountTier)
		}

		res := map[string]any{
			"worker_id":         w.ID,
			"tier":              w.Tier,
			"discovered_models": len(newModels),
		}
		if err != nil {
			res["error"] = err.Error()
		}
		workerResults = append(workerResults, res)
	}

	return map[string]any{
		"workers_checked":  len(workersList),
		"models_found":    discoveredCount,
		"workers":          workerResults,
		"active_registry": gemini.GetModelRegistry().GetAll(),
	}, nil
}
