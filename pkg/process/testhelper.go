// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Tetragon

package process

import (
	"github.com/cilium/tetragon/pkg/defaults"
	"github.com/cilium/tetragon/pkg/watcher"
)

// InitCacheForTest initializes the process cache with default stale
// parameters. Intended for tests and benchmarks.
func InitCacheForTest(w watcher.PodAccessor, size int) error {
	return InitCache(w, size,
		defaults.DefaultProcessCacheGCInterval,
		defaults.DefaultProcessCacheStaleInterval,
		defaults.DefaultProcessCacheStaleThreshold,
		defaults.DefaultProcessCacheStaleBackoffMultiplier,
	)
}
