package bot

import (
	"fmt"
	"sync"
	"time"
)

// reviewDebouncer collapses the burst of webhooks one user action can fire
// (e.g. "opened" plus "synchronized", or a GitLab "update" for a title edit
// at the same commit) into a single automatic review.
//
// An automatic review is claimed per bot, pull request and head commit: the
// first trigger wins and further triggers for the same commit within the
// window are dropped. A review that was skipped or failed gives its claim
// back, closing the pull request ends the window, a payload without a head
// commit is never debounced, and explicit review requests do not go through
// here. The state is in memory and per process.
type reviewDebouncer struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	now     func() time.Time
	entries map[string]reviewClaim
}

type reviewClaim struct {
	sha string
	at  time.Time
}

func newReviewDebouncer() *reviewDebouncer {
	return &reviewDebouncer{window: 60 * time.Second, max: 5000, now: time.Now, entries: map[string]reviewClaim{}}
}

func debounceKey(botID int64, repoFullName string, prNum int64) string {
	return fmt.Sprintf("%d|%s|%d", botID, repoFullName, prNum)
}

// tryClaim reports whether the review of sha should run.
func (d *reviewDebouncer) tryClaim(key, sha string) bool {
	if sha == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if prev, ok := d.entries[key]; ok && prev.sha == sha && now.Before(prev.at.Add(d.window)) {
		return false
	}
	if len(d.entries) >= d.max {
		d.evictOldest()
	}
	d.entries[key] = reviewClaim{sha: sha, at: now}
	return true
}

// release gives the claim on sha back, so a retry right away runs. A claim
// since taken for another commit is left alone.
func (d *reviewDebouncer) release(key, sha string) {
	if sha == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.entries[key]; ok && cur.sha == sha {
		delete(d.entries, key)
	}
}

// forget ends the pull request's window: it was closed, so a reopen is a new request.
func (d *reviewDebouncer) forget(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, key)
}

func (d *reviewDebouncer) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for k, v := range d.entries {
		if oldestKey == "" || v.at.Before(oldest) {
			oldestKey, oldest = k, v.at
		}
	}
	delete(d.entries, oldestKey)
}
