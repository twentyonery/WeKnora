// Package sandbox: per-(tenant, config) live-sandbox quota.
//
// The quota answers one question: how many sandboxes may this workspace keep
// alive across all of its sessions at once. Sessions can be many; provider
// sandboxes are what actually cost money and file descriptors, so the cap is
// counted per (tenant, sandbox config) pair over every binding this process
// creates, not per session.
//
// Counting is deliberately in-process. The binding store is already the
// authoritative cross-process record, but it has no atomic
// "count-and-create" primitive on every supported backend, so the limiter is
// a fast local guard against runaway creation from one process. Single-node
// deployments — the overwhelmingly common case for self-hosted WeKnora — get
// exact enforcement; multi-replica deployments get per-replica caps, which is
// why MaxConcurrentSandboxes documents that operators should account for
// replica count.
package sandbox

import (
	"errors"
	"sync"
)

// ErrTenantSandboxLimitExceeded reports that creating one more sandbox for
// this tenant + config would exceed MaxConcurrentSandboxes. Callers should
// surface it as a user-facing quota message, not an internal failure.
var ErrTenantSandboxLimitExceeded = errors.New(
	"sandbox: workspace concurrent sandbox limit reached, destroy an existing sandbox or raise the limit",
)

// tenantSandboxSlot identifies one counted slot holder.
type tenantSandboxSlot struct {
	tenantID uint64
	configID string
}

// TenantSandboxLimiter counts live sandboxes per (tenant, config) pair.
// It is safe for concurrent use and designed to be shared process-wide.
type TenantSandboxLimiter struct {
	mu   sync.Mutex
	live map[tenantSandboxSlot]int
}

// NewTenantSandboxLimiter returns an empty limiter.
func NewTenantSandboxLimiter() *TenantSandboxLimiter {
	return &TenantSandboxLimiter{live: make(map[tenantSandboxSlot]int)}
}

// Acquire reserves one create slot. A limit of zero or less means unlimited
// and always succeeds. The error is ErrTenantSandboxLimitExceeded once the
// configured cap is already held.
func (l *TenantSandboxLimiter) Acquire(tenantID uint64, configID string, limit int) error {
	if limit <= 0 {
		return nil
	}
	slot := tenantSandboxSlot{tenantID: tenantID, configID: configID}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.live[slot] >= limit {
		return ErrTenantSandboxLimitExceeded
	}
	l.live[slot]++
	return nil
}

// Release returns one slot. Releases without a matching acquire (e.g. for a
// recovered pre-existing sandbox that never consumed a slot) are clamped at
// zero so adoption paths cannot drive the counter negative.
func (l *TenantSandboxLimiter) Release(tenantID uint64, configID string) {
	slot := tenantSandboxSlot{tenantID: tenantID, configID: configID}
	l.mu.Lock()
	defer l.mu.Unlock()
	if remaining := l.live[slot] - 1; remaining > 0 {
		l.live[slot] = remaining
	} else {
		delete(l.live, slot)
	}
}

// Live reports the currently held slot count, for tests and diagnostics.
func (l *TenantSandboxLimiter) Live(tenantID uint64, configID string) int {
	slot := tenantSandboxSlot{tenantID: tenantID, configID: configID}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live[slot]
}
