// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Tetragon

package process

import (
	"fmt"
	"maps"
	"path/filepath"
	"time"

	"github.com/cilium/ebpf"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/errgroup"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/cilium/tetragon/pkg/defaults"
	"github.com/cilium/tetragon/pkg/logger"
	"github.com/cilium/tetragon/pkg/logger/logfields"
	"github.com/cilium/tetragon/pkg/sensors/exec/execvemap"
)

type Cache struct {
	cache             *lru.Cache[string, *ProcessInternal]
	size              int
	deleteChan        chan *ProcessInternal
	done              chan struct{}
	workers           *errgroup.Group
	staleThreshold    time.Duration
	backoffMultiplier float64
}

// garbage collection states
const (
	inUse = iota
	deletePending
	deleteReady
	deleted
)

var colorStr = map[int]string{
	inUse:         "inUse",
	deletePending: "deletePending",
	deleteReady:   "deleteReady",
	deleted:       "deleted",
}

func cacheProcessLogFields(p *ProcessInternal) []any {
	if p == nil || p.process == nil {
		return []any{"process.nil", true}
	}
	pid := p.process.GetPid()
	var pidValue uint32
	if pid != nil {
		pidValue = pid.GetValue()
	}
	tid := p.process.GetTid()
	var tidValue uint32
	if tid != nil {
		tidValue = tid.GetValue()
	}
	return []any{
		"process.exec_id", p.process.ExecId,
		"process.pid", pidValue,
		"process.tid", tidValue,
		"process.parent_exec_id", p.process.ParentExecId,
		"process.color", colorStr[p.color],
		"process.refcnt_ops_map", p.refcntOps,
		"process.lifecycle", p.Lifecycle().Snapshot(),
	}
}

func (pc *Cache) cacheGarbageCollector(intervalGC time.Duration) {
	ticker := time.NewTicker(intervalGC)
	pc.deleteChan = make(chan *ProcessInternal)
	pc.done = make(chan struct{})

	logger.Trace(logger.GetLogger(), "process cache GC started",
		"interval", intervalGC)

	pc.workers.Go(func() error {
		var deleteQueue []*ProcessInternal
		for {
			select {
			case <-pc.done:
				logger.Trace(logger.GetLogger(), "process cache GC stop requested")
				ticker.Stop()
				pc.cache.Purge()
				return nil
			case <-ticker.C:
				logger.Trace(logger.GetLogger(), "process cache GC tick",
					"queue_len", len(deleteQueue),
					"cache_len", pc.cache.Len())
				newQueue := []*ProcessInternal{}
				for _, p := range deleteQueue {
					// OOO bounce: a child exec arrived after this
					// process was queued for deletion, so CanRemove() is
					// false again. Reset color to inUse so a future
					// deletePending() can re-queue it via deleteChan
					// (otherwise it would be stuck forever).
					if !p.Lifecycle().CanRemove() && !p.Lifecycle().IsStale() {
						logger.Trace(logger.GetLogger(), "process cache GC skip: not removable",
							cacheProcessLogFields(p)...)
						p.color = inUse
						continue
					}
					if p.color == deleteReady {
						logger.Trace(logger.GetLogger(), "process cache GC delete ready",
							cacheProcessLogFields(p)...)
						p.color = deleted
						pc.remove(p.process)
					} else {
						logger.Trace(logger.GetLogger(), "process cache GC mark delete ready",
							cacheProcessLogFields(p)...)
						newQueue = append(newQueue, p)
						p.color = deleteReady
					}
				}
				deleteQueue = newQueue
			case p := <-pc.deleteChan:
				logger.Trace(logger.GetLogger(), "process cache GC delete requested",
					append(cacheProcessLogFields(p),
						"queue_len", len(deleteQueue))...)
				// Notice color is only ever touched inside GC behind
				// select channel logic so should be safe to work on
				// and assume its visible everywhere.

				// Already removed from cache — ignore.
				if p.color == deleted {
					logger.Trace(logger.GetLogger(), "process cache GC delete ignored: already deleted",
						cacheProcessLogFields(p)...)
					continue
				}
				// Already in the queue (deletePending or deleteReady).
				// Reset to deletePending so the GC keeps it alive for
				// at least another full pass before removal.
				if p.color != inUse {
					logger.Trace(logger.GetLogger(), "process cache GC duplicate delete",
						cacheProcessLogFields(p)...)
					p.color = deletePending
					continue
				}
				// Fresh entry — queue for deletion.
				logger.Trace(logger.GetLogger(), "process cache GC queue delete pending",
					append(cacheProcessLogFields(p),
						"queue_len", len(deleteQueue))...)
				p.color = deletePending
				deleteQueue = append(deleteQueue, p)
			}
		}
	})
}

func (pc *Cache) deletePending(process *ProcessInternal) {
	logger.Trace(logger.GetLogger(), "process cache delete pending",
		cacheProcessLogFields(process)...)
	pc.deleteChan <- process
}

func (pc *Cache) refDec(p *ProcessInternal, reason string) {
	p.refcntOpsLock.Lock()
	// count number of times refcnt is decremented for a specific reason (i.e. process, parent, etc.)
	p.refcntOps[reason]++
	opsCount := p.refcntOps[reason]
	p.refcntOpsLock.Unlock()
	logger.Trace(logger.GetLogger(), "process cache refcnt dec",
		append(cacheProcessLogFields(p),
			"reason", reason,
			"refcnt_ops", opsCount)...)
}

func (pc *Cache) refInc(p *ProcessInternal, reason string) {
	p.refcntOpsLock.Lock()
	// count number of times refcnt is incremented for a specific reason (i.e. process, parent, etc.)
	p.refcntOps[reason]++
	opsCount := p.refcntOps[reason]
	p.refcntOpsLock.Unlock()
	logger.Trace(logger.GetLogger(), "process cache refcnt inc",
		append(cacheProcessLogFields(p),
			"reason", reason,
			"refcnt_ops", opsCount)...)
}

func (pc *Cache) purge() {
	close(pc.done)
	pc.workers.Wait()
	processCacheTotal.Set(0)
}

func NewCache(
	processCacheSize int,
	GCInterval time.Duration,
	staleTickInterval time.Duration,
	staleThreshold time.Duration,
	staleBackoffMultiplier float64,
) (*Cache, error) {
	// Stash a reference to the Cache to refer to later in the eviction closure.
	pm := &Cache{
		size:              processCacheSize,
		staleThreshold:    staleThreshold,
		backoffMultiplier: staleBackoffMultiplier,
	}

	lruCache, err := lru.NewWithEvict(
		processCacheSize,
		func(_ string, evicted *ProcessInternal) {
			processCacheEvictions.Inc()
			logger.Trace(logger.GetLogger(), "process cache LRU evict",
				cacheProcessLogFields(evicted)...)

			// Only check refcntOps for processes that completed the normal
			// GC lifecycle (color == deleted, !stale). For capacity evictions
			// (inUse) and stale-cleaned entries, mismatch is expected.
			skipWarn := evicted.color != deleted || evicted.lifecycle.IsStale()
			if !skipWarn {
				evicted.refcntOpsLock.Lock()
				procInc := evicted.refcntOps["process++"]
				procDec := evicted.refcntOps["process--"]
				parentInc := evicted.refcntOps["parent++"]
				parentDec := evicted.refcntOps["parent--"]
				evicted.refcntOpsLock.Unlock()
				if procInc != procDec || parentInc != parentDec {
					logger.GetLogger().Warn("process cache GC: refcntOps mismatch",
						append(cacheProcessLogFields(evicted),
							"process++", procInc,
							"process--", procDec,
							"parent++", parentInc,
							"parent--", parentDec)...)
				}
			}

			// Perform parent-- via ChildExit for LRU-evicted entries that will never
			// reach the exit handler.

			// Skip non-inUse entries whose exit path already performed ChildExit
			if evicted.color != inUse {
				return
			}

			// Is the parent still in the cache?
			if evicted.process == nil {
				return
			}
			parent, ok := pm.cache.Peek(evicted.process.ParentExecId)
			if !ok {
				return
			}
			parent.ChildExit(evicted.process.ExecId)
			logger.Trace(logger.GetLogger(), "process cache LRU evict parent ChildExit",
				cacheProcessLogFields(evicted)...)
		},
	)
	if err != nil {
		return nil, err
	}

	pm.cache = lruCache
	pm.workers = &errgroup.Group{}
	pm.cacheGarbageCollector(GCInterval)
	pm.startStaleCleaner(staleTickInterval)
	return pm, nil
}

// touchProcess updates stale-tracking fields when an event is received for
// a process. Called from get() and add().
func (pc *Cache) touchProcess(p *ProcessInternal) {
	p.lifecycle.SetLastEvent(uint32(time.Now().Unix()))
	p.lifecycle.SetStaleInterval(uint32(pc.staleThreshold.Seconds()))
	p.lifecycle.ClearStale()
}

func (pc *Cache) get(processID string) (*ProcessInternal, error) {
	process, ok := pc.cache.Get(processID)
	if !ok {
		logger.GetLogger().Debug("process not found in cache", "id", processID)
		processCacheMisses.WithLabelValues("get").Inc()
		return nil, fmt.Errorf("invalid entry for process ID: %s", processID)
	}
	pc.touchProcess(process)
	logger.Trace(logger.GetLogger(), "process cache get hit",
		cacheProcessLogFields(process)...)
	return process, nil
}

// add inserts a ProcessInternal structure into the cache. Must be called only
// from clone or execve events. Returns (duplicate, evicted):
//   - duplicate=true means the exec_id already existed and the new entry was NOT added.
//   - evicted=true means adding the new entry caused an LRU capacity eviction.
func (pc *Cache) add(process *ProcessInternal) (duplicate, evicted bool) {
	pc.touchProcess(process)
	found, evicted := pc.cache.ContainsOrAdd(process.process.ExecId, process)
	if found {
		logger.GetLogger().Warn("process cache: duplicate add ignored",
			"exec_id", process.process.ExecId)
		return true, false
	}
	if !evicted {
		processCacheTotal.Inc()
	} else {
		processCacheCapacityEvictions.Inc()
	}
	logger.Trace(logger.GetLogger(), "process cache add",
		append(cacheProcessLogFields(process),
			"evicted", evicted,
			"cache_len", pc.cache.Len())...)
	return false, evicted
}

func (pc *Cache) remove(process *tetragon.Process) bool {
	present := pc.cache.Remove(process.ExecId)
	if present {
		processCacheTotal.Dec()
	} else {
		processCacheMisses.WithLabelValues("remove").Inc()
	}
	pid := process.GetPid()
	var pidValue uint32
	if pid != nil {
		pidValue = pid.GetValue()
	}
	logger.Trace(logger.GetLogger(), "process cache remove",
		"process.exec_id", process.ExecId,
		"process.pid", pidValue,
		"present", present,
		"cache_len", pc.cache.Len())
	return present
}

func (pc *Cache) len() int {
	return pc.cache.Len()
}

// startStaleCleaner starts a goroutine that periodically scans the cache for
// stale processes (no events for > staleInterval) and marks them for GC removal
// by cross-checking against the BPF execve_map.
func (pc *Cache) startStaleCleaner(interval time.Duration) {
	ticker := time.NewTicker(interval)

	logger.GetLogger().Info("process cache stale cleaner started",
		"interval", interval)

	pc.workers.Go(func() error {
		defer ticker.Stop()
		for {
			select {
			case <-pc.done:
				logger.GetLogger().Info("process cache stale cleaner stopped")
				return nil
			case <-ticker.C:
				pc.staleCleaner()
			}
		}
	})
}

func (pc *Cache) staleCleaner() {
	start := time.Now()
	now := uint32(time.Now().Unix())
	defThreshold := uint32(pc.staleThreshold.Seconds())

	execveMapPath := filepath.Join(defaults.DefaultMapRoot, defaults.DefaultMapPrefix, "execve_map")
	var execveMap *ebpf.Map

	var checked, cleaned int
	for _, p := range pc.cache.Values() {
		if p.process == nil {
			continue
		}

		lastEvent := p.lifecycle.LastEvent()

		// BREAK: if even the minimum interval hasn't elapsed, all
		// subsequent entries are newer (LRU order = lastEvent order).
		if lastEvent+defThreshold > now {
			break
		}

		// Per-process deadline using its (possibly doubled) staleInterval.
		deadline := lastEvent + p.lifecycle.StaleInterval()
		if now < deadline {
			continue
		}

		// Lazy-load execve_map on first stale candidate to avoid
		// file I/O on ticks with no candidates.
		if execveMap == nil {
			var err error
			execveMap, err = ebpf.LoadPinnedMap(execveMapPath, &ebpf.LoadPinOptions{ReadOnly: true})
			if err != nil {
				logger.GetLogger().Debug("stale cleaner: failed to open execve_map, skipping tick",
					logfields.Error, err)
				break
			}
			defer execveMap.Close()
		}

		checked++
		pid := p.process.Pid.GetValue()

		var val execvemap.ExecveValue
		if err := execveMap.Lookup(&execvemap.ExecveKey{Pid: pid}, &val); err != nil {
			// CASE 1: PID not found in execve_map → process is dead.
			logger.GetLogger().Debug("stale cleaner: pid not in execve_map, marking stale",
				"exec_id", p.process.ExecId,
				"pid", pid)
		} else if ktime, ktimeErr := KtimeFromExecID(p.process.ExecId); ktimeErr != nil {
			logger.GetLogger().Warn("stale cleaner: failed to parse ktime from exec_id",
				"exec_id", p.process.ExecId,
				logfields.Error, ktimeErr)
		} else if val.Process.Ktime != ktime {
			// CASE 2: PID recycled → our process is dead.
			logger.GetLogger().Debug("stale cleaner: pid recycled, marking stale",
				"exec_id", p.process.ExecId,
				"pid", pid,
				"cached_ktime", ktime,
				"bpf_ktime", val.Process.Ktime)
		} else {
			// CASE 3: same process, still alive in BPF → backoff.
			p.lifecycle.SetStaleInterval(uint32(float64(p.lifecycle.StaleInterval()) * pc.backoffMultiplier))
			logger.GetLogger().Debug("stale cleaner: process alive in BPF, backing off",
				"exec_id", p.process.ExecId,
				"pid", pid,
				"new_interval", p.lifecycle.StaleInterval())
			continue
		}

		// Notify parent that this child is being force-removed.
		// Use Peek to avoid resetting the parent's stale timer.
		if p.process.ParentExecId != "" {
			if parent, ok := pc.cache.Peek(p.process.ParentExecId); ok {
				parent.ChildExit(p.process.ExecId)
			}
		}
		p.lifecycle.MarkStale()
		pc.deletePending(p)
		processCacheStaleCleaned.Inc()
		cleaned++
	}

	dur := time.Since(start)
	logger.GetLogger().Debug("process cache stale cleaner tick",
		"checked", checked,
		"cleaned", cleaned,
		"duration", dur,
		"cache_len", pc.cache.Len())
	if dur > 10*time.Second {
		logger.GetLogger().Warn("process cache stale cleaner slow",
			"duration", dur, "checked", checked, "cleaned", cleaned)
	}
}

func (pc *Cache) dump(opts *tetragon.DumpProcessCacheReqArgs) []*tetragon.ProcessInternal {
	execveMapPath := filepath.Join(defaults.DefaultMapRoot, defaults.DefaultMapPrefix, "execve_map")
	var execveMap *ebpf.Map
	var err error
	if opts.ExcludeExecveMapProcesses {
		execveMap, err = ebpf.LoadPinnedMap(execveMapPath, &ebpf.LoadPinOptions{ReadOnly: true})
		if err != nil {
			logger.GetLogger().Warn("failed to open execve_map", logfields.Error, err)
			return []*tetragon.ProcessInternal{}
		}
		defer execveMap.Close()
	}

	var processes []*tetragon.ProcessInternal
	for _, v := range pc.cache.Values() {
		if opts.SkipRemovable && v.Lifecycle().CanRemove() {
			continue
		}
		if opts.ExcludeExecveMapProcesses {
			var val execvemap.ExecveValue
			if err := execveMap.Lookup(&execvemap.ExecveKey{Pid: v.process.Pid.Value}, &val); err == nil {
				ktime, ktimeErr := KtimeFromExecID(v.process.ExecId)
				if ktimeErr == nil && val.Process.Ktime == ktime {
					continue
				}
			}
		}
		processes = append(processes, &tetragon.ProcessInternal{
			Process:   proto.Clone(v.process).(*tetragon.Process),
			Refcnt:    &wrapperspb.UInt32Value{Value: v.RefcntOpsSum()},
			RefcntOps: maps.Clone(v.refcntOps),
			Color:     colorStr[v.color],
			Lifecycle: toStruct(v.Lifecycle().Snapshot()),
		})
	}
	return processes
}

func toStruct(m map[string]any) *structpb.Struct {
	s, _ := structpb.NewStruct(m)
	return s
}

func (pc *Cache) getEntries() []*tetragon.ProcessInternal {
	var processes []*tetragon.ProcessInternal
	for _, v := range pc.cache.Values() {
		processes = append(processes, &tetragon.ProcessInternal{
			Process:   v.process,
			Refcnt:    &wrapperspb.UInt32Value{Value: v.RefcntOpsSum()},
			RefcntOps: v.refcntOps,
			Color:     colorStr[v.color],
			Lifecycle: toStruct(v.Lifecycle().Snapshot()),
		})
	}
	return processes
}
