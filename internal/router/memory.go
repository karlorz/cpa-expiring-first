package router

import (
	"sync"
	"time"
)

// MemoryQuota holds quota readings the plugin took itself, in memory only. Probes write whole
// snapshots into it; picks read them without touching the network.
type MemoryQuota struct {
	mu    sync.Mutex
	quota Quota
}

// NewMemoryQuota returns an empty in-memory quota source.
func NewMemoryQuota() *MemoryQuota {
	return &MemoryQuota{}
}

// Replace swaps in a whole snapshot of probe readings. A nil snapshot clears the readings.
func (m *MemoryQuota) Replace(quota Quota) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quota = quota
}

// Load implements QuotaSource. It returns the last snapshot, or nil when no probe has
// reported yet.
func (m *MemoryQuota) Load() Quota {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quota
}

// OverlayQuota merges an optional file source over in-memory probe readings. A file reading
// wins for one auth ID while it is younger than maxAge, so an external producer can override
// individual accounts; otherwise the probe reading is used. Without a file source, or while
// the file holds nothing, the memory readings are used as they are.
type OverlayQuota struct {
	memory QuotaSource
	file   QuotaSource
	maxAge time.Duration
	now    func() time.Time
}

// NewOverlayQuota returns a source that prefers file readings younger than maxAge over memory.
// A nil memory source makes the file the only source.
func NewOverlayQuota(memory, file QuotaSource, maxAge time.Duration) *OverlayQuota {
	return &OverlayQuota{memory: memory, file: file, maxAge: maxAge, now: time.Now}
}

// Load implements QuotaSource. It returns nil when neither source has a reading, so the
// scheduler falls back to sticky routing.
func (o *OverlayQuota) Load() Quota {
	var memory Quota
	if o.memory != nil {
		memory = o.memory.Load()
	}
	var file Quota
	if o.file != nil {
		file = o.file.Load()
	}
	if len(file) == 0 {
		return memory
	}
	now := o.now()
	merged := make(Quota, len(memory)+len(file))
	for authID, account := range memory {
		merged[authID] = account
	}
	for authID, account := range file {
		if account.UpdatedAt.IsZero() || now.Sub(account.UpdatedAt) > o.maxAge {
			continue
		}
		merged[authID] = account
	}
	return merged
}
