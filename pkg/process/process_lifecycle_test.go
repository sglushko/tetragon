// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Tetragon

package process

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/cilium/tetragon/api/v1/tetragon"
)

// touchEvent is a test helper that simulates what Cache.touchProcess does.
func touchEvent(lc ProcessLifecycler, thresholdSec uint32) {
	lc.SetLastEvent(uint32(time.Now().Unix()))
	lc.SetStaleInterval(thresholdSec)
	lc.ClearStale()
}

// initTestCache initializes the global procCache for tests.
// testStaleThresholdSec is the default stale threshold used in tests (60 min).
const testStaleThresholdSec = uint32(3600)

func initTestCache(t *testing.T) {
	t.Helper()
	err := InitCacheForTest(nil, 10)
	require.NoError(t, err)
	t.Cleanup(func() { FreeCache() })
}

// newTestProcess creates a ProcessInternal with lifecycle for testing.
func newTestProcess(t *testing.T, execID string, pid uint32) *ProcessInternal {
	t.Helper()
	return &ProcessInternal{
		process: &tetragon.Process{
			ExecId: execID,
			Pid:    &wrapperspb.UInt32Value{Value: pid},
		},
		refcntOps: make(map[string]int32),
		lifecycle: NewLifecycle(),
	}
}

func TestProcessExit_FirstExit_NoChildren(t *testing.T) {
	initTestCache(t)

	proc := newTestProcess(t, "proc1", 100)
	procCache.add(proc)

	// First exit: should succeed (no children, immediately removable).
	stateChanged, removable := proc.ProcessExit()
	assert.True(t, stateChanged, "first exit should change state")
	assert.True(t, removable, "first exit with no children should be removable")

	// Verify RefDec("process") was tracked.
	assert.Equal(t, int32(1), proc.refcntOps["process--"])
}

func TestProcessExit_DuplicateExit_StillRemovable(t *testing.T) {
	initTestCache(t)

	proc := newTestProcess(t, "proc2", 101)
	procCache.add(proc)

	// First exit.
	stateChanged1, removable1 := proc.ProcessExit()
	assert.True(t, stateChanged1, "first exit should change state")
	assert.True(t, removable1)

	// Duplicate exit: exitSeen already true, CanRemove still true → should return true.
	stateChanged2, removable2 := proc.ProcessExit()
	assert.False(t, stateChanged2, "duplicate exit should not change state")
	assert.True(t, removable2, "duplicate exit should still be removable when CanRemove is true")

	// RefDec("process") should have happened exactly once (CAS-guarded).
	assert.Equal(t, int32(1), proc.refcntOps["process--"])
}

func TestProcessExit_WithChildren_DefersDeletion(t *testing.T) {
	initTestCache(t)

	proc := newTestProcess(t, "proc3", 102)
	procCache.add(proc)

	// Simulate a child being added.
	assert.True(t, proc.ChildExec("child1"), "first ChildExec should be new")

	// Exit with active children: CanRemove should be false.
	stateChanged, removable := proc.ProcessExit()
	assert.True(t, stateChanged, "first exit should change state")
	assert.False(t, removable, "exit with active children should not be removable")
	assert.Equal(t, int32(1), proc.refcntOps["process--"], "RefDec should still happen on first exit")
}

func TestProcessExit_ChildrenExit_ThenDuplicateExit(t *testing.T) {
	initTestCache(t)

	proc := newTestProcess(t, "proc4", 103)
	procCache.add(proc)

	// Add child, mark exit.
	assert.True(t, proc.ChildExec("child1"), "first ChildExec should be new")
	stateChanged1, removable1 := proc.ProcessExit()
	assert.True(t, stateChanged1, "first exit should change state")
	assert.False(t, removable1, "should not be removable: has active children")

	// Child exits.
	childChanged, childRemovable := proc.ChildExit("child1")
	assert.True(t, childChanged, "known child exit should change state")
	assert.True(t, childRemovable, "should be removable: exited + no children")

	// Duplicate exit (e.g., cleanup event): exitSeen already true, now CanRemove is true.
	stateChanged2, removable2 := proc.ProcessExit()
	assert.False(t, stateChanged2, "duplicate exit should not change state")
	assert.True(t, removable2, "should be removable after children exited")

	// RefDec happened only once.
	assert.Equal(t, int32(1), proc.refcntOps["process--"])
}

func TestChildExec_Idempotent(t *testing.T) {
	lc := NewLifecycle()

	assert.True(t, lc.ChildExec("child1"), "first ChildExec should return true")
	assert.False(t, lc.ChildExec("child1"), "duplicate ChildExec should return false")

	snap := lc.Snapshot()
	assert.Equal(t, 1, snap["child_refs"], "duplicate ChildExec should not double-count")
	assert.Equal(t, []any{"child1"}, snap["children"])
}

func TestChildExit_UnknownChild(t *testing.T) {
	lc := NewLifecycle()

	// Removing unknown child should not panic or change state.
	assert.False(t, lc.ChildExit("nonexistent"), "unknown child should return false")

	snap := lc.Snapshot()
	assert.Equal(t, 0, snap["child_refs"])
	assert.Nil(t, snap["children"])
}

func TestChildExit_KnownChild(t *testing.T) {
	lc := NewLifecycle()
	lc.ChildExec("child1")

	assert.True(t, lc.ChildExit("child1"), "known child should return true")
	assert.False(t, lc.ChildExit("child1"), "second ChildExit should return false")
}

func TestCanRemove_WithChildren(t *testing.T) {
	lc := NewLifecycle()

	lc.ChildExec("child1")
	lc.ChildExec("child2")
	lc.MarkExited()

	assert.False(t, lc.CanRemove(), "should not be removable with active children")

	lc.ChildExit("child1")
	assert.False(t, lc.CanRemove(), "should not be removable with one child remaining")

	lc.ChildExit("child2")
	assert.True(t, lc.CanRemove(), "should be removable after all children exited")
}

func TestChildExec_BundlesRefInc(t *testing.T) {
	initTestCache(t)

	parent := newTestProcess(t, "parent1", 200)
	procCache.add(parent)

	parent.ChildExec("child1")
	parent.ChildExec("child1") // duplicate — should not RefInc again

	assert.Equal(t, int32(1), parent.refcntOps["parent++"], "ChildExec should RefInc exactly once per child")
	snap := parent.Lifecycle().Snapshot()
	assert.Equal(t, 1, snap["child_refs"])
	assert.Equal(t, []any{"child1"}, snap["children"])
}

func TestChildExit_BundlesRefDecAndTryRemove(t *testing.T) {
	initTestCache(t)

	parent := newTestProcess(t, "parent2", 201)
	procCache.add(parent)

	parent.ChildExec("child1")
	parent.ProcessExit() // marks exited + refDec("process--")

	parent.ChildExit("child1")

	assert.Equal(t, int32(1), parent.refcntOps["parent--"], "ChildExit should call RefDec")
	snap := parent.Lifecycle().Snapshot()
	assert.Equal(t, 0, snap["child_refs"])
	assert.Nil(t, snap["children"])
}

func TestSnapshot_IncludesChildren(t *testing.T) {
	lc := NewLifecycle()
	lc.ChildExec("child-a")
	lc.ChildExec("child-b")

	snap := lc.Snapshot()
	assert.Equal(t, 2, snap["child_refs"])
	assert.ElementsMatch(t, []any{"child-a", "child-b"}, snap["children"])
	assert.False(t, snap["exit_seen"].(bool))
}

func TestTouchEvent_SetsLastEventAndInterval(t *testing.T) {
	lc := NewLifecycle()

	// Before touch, lastEvent and staleInterval are zero.
	assert.Equal(t, uint32(0), lc.LastEvent())
	assert.Equal(t, uint32(0), lc.StaleInterval())

	touchEvent(lc, testStaleThresholdSec)

	// After touch, lastEvent should be close to now.
	assert.NotEqual(t, uint32(0), lc.LastEvent())
	assert.Equal(t, testStaleThresholdSec, lc.StaleInterval())
	assert.False(t, lc.IsStale())
}

func TestTouchEvent_ResetsStaleAndInterval(t *testing.T) {
	lc := NewLifecycle()
	touchEvent(lc, testStaleThresholdSec)

	// Simulate stale marking and backoff.
	lc.MarkStale()
	lc.SetStaleInterval(lc.StaleInterval() * 2)
	assert.True(t, lc.IsStale())
	assert.Equal(t, testStaleThresholdSec*2, lc.StaleInterval())

	// touchEvent resets everything.
	touchEvent(lc, testStaleThresholdSec)
	assert.False(t, lc.IsStale())
	assert.Equal(t, testStaleThresholdSec, lc.StaleInterval())
}

func TestMarkStale_ClearStale(t *testing.T) {
	lc := NewLifecycle()

	assert.False(t, lc.IsStale())
	lc.MarkStale()
	assert.True(t, lc.IsStale())
	lc.ClearStale()
	assert.False(t, lc.IsStale())
}

func TestStaleInterval_Backoff(t *testing.T) {
	lc := NewLifecycle()
	touchEvent(lc, testStaleThresholdSec)

	base := lc.StaleInterval()
	assert.Equal(t, testStaleThresholdSec, base)

	// Simulate backoff doubling.
	lc.SetStaleInterval(base * 2)
	assert.Equal(t, base*2, lc.StaleInterval())

	lc.SetStaleInterval(lc.StaleInterval() * 2)
	assert.Equal(t, base*4, lc.StaleInterval())

	// touchEvent resets to default.
	touchEvent(lc, testStaleThresholdSec)
	assert.Equal(t, testStaleThresholdSec, lc.StaleInterval())
}

func TestConfigStaleInterval(t *testing.T) {
	// Verify that touchEvent sets the provided threshold.
	lc := NewLifecycle()
	touchEvent(lc, 7200) // 2 hours
	assert.Equal(t, uint32(7200), lc.StaleInterval())
}

func TestSnapshot_IncludesStaleFields(t *testing.T) {
	lc := NewLifecycle()
	touchEvent(lc, testStaleThresholdSec)
	lc.MarkStale()
	lc.SetStaleInterval(9999)

	snap := lc.Snapshot()
	assert.True(t, snap["stale"].(bool))
	assert.NotEqual(t, 0, snap["last_event"])
	assert.Equal(t, 9999, snap["stale_interval"])
}

func TestIsStale_GCBypass(t *testing.T) {
	initTestCache(t)

	proc := newTestProcess(t, "stale-proc", 999)
	procCache.add(proc)

	// Not stale — GC should respect CanRemove.
	assert.False(t, proc.Lifecycle().IsStale())
	assert.False(t, proc.Lifecycle().CanRemove())

	// Mark stale — GC should bypass CanRemove.
	proc.Lifecycle().MarkStale()
	assert.True(t, proc.Lifecycle().IsStale())
	// CanRemove is still false (no exit), but IsStale overrides in GC.
	assert.False(t, proc.Lifecycle().CanRemove())
}

func TestCacheGet_CallsTouchProcess(t *testing.T) {
	initTestCache(t)

	proc := newTestProcess(t, "touch-get", 888)
	procCache.add(proc)

	// Mark stale to verify get() clears it via touchProcess.
	proc.Lifecycle().MarkStale()
	assert.True(t, proc.Lifecycle().IsStale())

	_, err := procCache.get("touch-get")
	require.NoError(t, err)

	// touchProcess should have cleared stale.
	assert.False(t, proc.Lifecycle().IsStale())
	assert.NotEqual(t, uint32(0), proc.Lifecycle().LastEvent())
}

func TestCacheAdd_CallsTouchProcess(t *testing.T) {
	initTestCache(t)

	proc := newTestProcess(t, "touch-add", 777)

	// Before add, lastEvent is zero.
	assert.Equal(t, uint32(0), proc.Lifecycle().LastEvent())

	procCache.add(proc)

	// After add, touchProcess should have set lastEvent.
	assert.NotEqual(t, uint32(0), proc.Lifecycle().LastEvent())
	assert.Equal(t, testStaleThresholdSec, proc.Lifecycle().StaleInterval())
}
