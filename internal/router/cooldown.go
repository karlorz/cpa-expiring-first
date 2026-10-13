package router

import (
	"time"
)

// StaleCooldown is a quota cooldown that a newer quota reading shows to be over, typically
// after a limit reset was spent on an exhausted account.
type StaleCooldown struct {
	AuthID    string
	AuthIndex string
	// Until is when the host would otherwise make the credential available again.
	Until time.Time
	// ReadAt is when the quota reading that shows quota left was taken.
	ReadAt time.Time
}

type cooldownKey struct {
	authID string
	until  time.Time
}

// StaleCooldowns returns the quota cooldowns the host can clear because a quota reading taken
// after the cooldown began shows quota left on every window.
//
// The host does not report when a cooldown began, so the scheduler records when it first saw
// each one. That moment is never earlier than the real start, so a reading newer than it was
// taken after the account ran out, and cannot be the stale reading from just before the 429.
// Only cooldowns longer than QuotaCooldownFloor count: shorter ones are transient or auth
// errors, which a quota reading says nothing about.
func (s *Scheduler) StaleCooldowns(states map[string]AuthState, now time.Time) []StaleCooldown {
	s.mu.Lock()
	cfg, source := s.cfg, s.quota
	s.mu.Unlock()
	if !cfg.ClearCooldowns() || source == nil {
		return nil
	}
	quota := source.Load()

	s.cooldownMu.Lock()
	defer s.cooldownMu.Unlock()
	seen := make(map[cooldownKey]time.Time, len(s.cooldownSeen))
	var stale []StaleCooldown
	for authID, state := range states {
		if state.Disabled || state.AuthIndex == "" || state.NextRetryAfter.Sub(now) <= QuotaCooldownFloor {
			continue
		}
		key := cooldownKey{authID: authID, until: state.NextRetryAfter}
		firstSeen, known := s.cooldownSeen[key]
		if !known {
			firstSeen = now
		}
		seen[key] = firstSeen

		account, read := quota[authID]
		if !read || now.Sub(account.UpdatedAt) > cfg.MaxAge() || !account.UpdatedAt.After(firstSeen) || !hasQuotaLeft(account, now) {
			continue
		}
		stale = append(stale, StaleCooldown{AuthID: authID, AuthIndex: state.AuthIndex, Until: state.NextRetryAfter, ReadAt: account.UpdatedAt})
	}
	s.cooldownSeen = seen
	return stale
}

// hasQuotaLeft reports whether every window of the reading has quota left or has reset. A
// reading without windows proves nothing.
func hasQuotaLeft(account QuotaAccount, now time.Time) bool {
	if len(account.Windows) == 0 {
		return false
	}
	for _, w := range account.Windows {
		if w.ResetsAt.After(now) && w.UsedPercent >= 100 {
			return false
		}
	}
	return true
}
