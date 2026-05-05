// Package approval holds pending deploy approvals in memory. Lifetime is
// process-bound — a restart drops every entry (auto-deny semantics).
package approval

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type Pending struct {
	ID        string    // 16-byte hex, used in TG callback_data
	Service   string    // "backend" | "frontend"
	Env       string    // "dev" | "prod"
	Image     string    // full image ref with tag
	CreatedAt time.Time

	// TGMessageID — id of the approval message we sent, used to edit it
	// on approve/deny/done. Chat id is global, not stored here.
	TGMessageID int

	// resolved becomes true once approve/deny/timeout has been processed
	// exactly once. Guards against double-clicks and reaper races.
	resolved bool
}

type PendingStore struct {
	mutex   sync.Mutex
	entries map[string]*Pending
	timeout time.Duration
}

func NewStore(timeout time.Duration) *PendingStore {
	return &PendingStore{
		entries: make(map[string]*Pending),
		timeout: timeout,
	}
}

func (store *PendingStore) Add(pending *Pending) *Pending {
	pending.ID = newID()
	pending.CreatedAt = time.Now().UTC()
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.entries[pending.ID] = pending
	return pending
}

func (store *PendingStore) SetMessageID(id string, messageID int) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if pending, ok := store.entries[id]; ok {
		pending.TGMessageID = messageID
	}
}

func (store *PendingStore) Get(id string) (*Pending, bool) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	pending, ok := store.entries[id]
	if !ok || pending.resolved {
		return nil, false
	}
	pendingCopy := *pending
	return &pendingCopy, true
}

// Resolve atomically marks the entry resolved; the second concurrent call
// for the same id returns ok=false. This is what makes double-clicks and
// reaper-vs-tap races safe.
func (store *PendingStore) Resolve(id string) (*Pending, bool) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	pending, ok := store.entries[id]
	if !ok || pending.resolved {
		return nil, false
	}
	pending.resolved = true
	pendingCopy := *pending
	return &pendingCopy, true
}

// Expired returns timed-out entries AND marks them resolved as it walks.
// Caller posts the timeout notification.
func (store *PendingStore) Expired(now time.Time) []*Pending {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	var expired []*Pending
	for _, pending := range store.entries {
		if pending.resolved {
			continue
		}
		if now.Sub(pending.CreatedAt) >= store.timeout {
			pending.resolved = true
			pendingCopy := *pending
			expired = append(expired, &pendingCopy)
		}
	}
	return expired
}

// Sweep drops resolved entries older than 2×timeout so the map doesn't
// grow forever. 2× keeps the double-click protection window intact.
func (store *PendingStore) Sweep(now time.Time) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	cutoff := store.timeout * 2
	for id, pending := range store.entries {
		if pending.resolved && now.Sub(pending.CreatedAt) > cutoff {
			delete(store.entries, id)
		}
	}
}

func newID() string {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		// crypto/rand failing → panic, never return a predictable id.
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(randomBytes[:])
}
