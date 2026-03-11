// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Tetragon

package process

import (
	"sync"
	"sync/atomic"
)


// ProcessLifecycler defines the lifecycle contract for process cache eviction.
// The default implementation tracks exit status and child references.
type ProcessLifecycler interface {
	// MarkExited records that an exit/cleanup event has been seen.
	// Returns true on the first call (state changed), false on subsequent calls.
	MarkExited() bool
	// ChildExec records that a child process was exec'd.
	// Returns true if this is a new child (state changed), false if already known.
	ChildExec(childExecId string) bool
	// ChildExit records that a child process exited.
	// Returns true if the child was known (state changed), false if unknown.
	ChildExit(childExecId string) bool
	// CanRemove returns true if the process is safe to remove from cache:
	// exit has been seen AND no active children remain.
	CanRemove() bool
	// Snapshot returns lifecycle state as a generic map for debug dumps.
	Snapshot() map[string]any

	// IsStale returns true if the process has been marked stale by the stale cleaner.
	IsStale() bool
	// MarkStale marks the process as stale for GC removal.
	MarkStale()
	// ClearStale clears the stale flag.
	ClearStale()
	// SetLastEvent sets the Unix epoch seconds of the last event for this process.
	SetLastEvent(epochSec uint32)
	// LastEvent returns the Unix epoch seconds of the last event received for this process.
	LastEvent() uint32
	// StaleInterval returns the current stale interval in seconds.
	StaleInterval() uint32
	// SetStaleInterval sets the stale interval in seconds (used for backoff).
	SetStaleInterval(sec uint32)
}

// NewLifecycle creates a new ProcessLifecycler instance.
var NewLifecycle func() ProcessLifecycler = func() ProcessLifecycler {
	return &ProcessLifecycle{
		children: make(map[string]struct{}),
	}
}

// ProcessLifecycle tracks the lifecycle state of a process for safe cache removal.
// Fields are safe to use from any goroutine: exitSeen is atomic, children is
// protected by mu.
type ProcessLifecycle struct {
	exitSeen      atomic.Bool
	stale         atomic.Bool   // true = marked stale by stale cleaner
	lastEvent     atomic.Uint32 // Unix epoch seconds of last event
	staleInterval atomic.Uint32 // seconds, doubles on backoff, reset by Cache.touchProcess
	mu            sync.Mutex
	children      map[string]struct{} // active children by execId (set)
}

// MarkExited records that an exit/cleanup event has been seen.
// Uses CAS for idempotency: returns true only on the first call.
func (l *ProcessLifecycle) MarkExited() bool {
	return l.exitSeen.CompareAndSwap(false, true)
}

// ChildExec records that a child process was exec'd.
// Called on the parent when a child process is added to the cache.
// Returns true if this is a new child, false if already tracked (idempotent).
func (l *ProcessLifecycle) ChildExec(childExecId string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.children[childExecId]; exists {
		return false
	}
	l.children[childExecId] = struct{}{}
	return true
}

// ChildExit records that a child process exited.
// Called on the parent when a child process becomes removable.
// Returns true if the child was known (state changed), false if unknown (idempotent).
func (l *ProcessLifecycle) ChildExit(childExecId string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.children[childExecId]; !exists {
		return false
	}
	delete(l.children, childExecId)
	return true
}

// CanRemove returns true if the process is safe to remove from the cache.
// A process is removable when its exit has been seen AND it has no active children.
func (l *ProcessLifecycle) CanRemove() bool {
	if !l.exitSeen.Load() {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.children) == 0
}

// IsStale returns true if the process has been marked stale by the stale cleaner.
func (l *ProcessLifecycle) IsStale() bool {
	return l.stale.Load()
}

// MarkStale marks the process as stale for GC removal.
func (l *ProcessLifecycle) MarkStale() {
	l.stale.Store(true)
}

// ClearStale clears the stale flag.
func (l *ProcessLifecycle) ClearStale() {
	l.stale.Store(false)
}

// SetLastEvent sets the Unix epoch seconds of the last event for this process.
func (l *ProcessLifecycle) SetLastEvent(epochSec uint32) {
	l.lastEvent.Store(epochSec)
}

// LastEvent returns the Unix epoch seconds of the last event received for this process.
func (l *ProcessLifecycle) LastEvent() uint32 {
	return l.lastEvent.Load()
}

// StaleInterval returns the current stale interval in seconds.
func (l *ProcessLifecycle) StaleInterval() uint32 {
	return l.staleInterval.Load()
}

// SetStaleInterval sets the stale interval in seconds (used for backoff).
func (l *ProcessLifecycle) SetStaleInterval(sec uint32) {
	l.staleInterval.Store(sec)
}

// Snapshot returns lifecycle state as a generic map for structured output.
func (l *ProcessLifecycle) Snapshot() map[string]any {
	l.mu.Lock()
	children := make([]any, 0, len(l.children))
	for id := range l.children {
		children = append(children, id)
	}
	l.mu.Unlock()
	m := map[string]any{
		"exit_seen":      l.exitSeen.Load(),
		"child_refs":     len(children),
		"stale":          l.stale.Load(),
		"last_event":     int(l.lastEvent.Load()),
		"stale_interval": int(l.staleInterval.Load()),
	}
	if len(children) > 0 {
		m["children"] = children
	}
	return m
}
