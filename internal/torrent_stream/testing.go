package torrent_stream

import "github.com/MunifTanjim/stremthru/internal/cache"

// ResetCachesForTesting isolates database fixtures from process-wide read/write
// caches. Call only from serial tests and pass t.Cleanup to restore the originals.
func ResetCachesForTesting(cleanup func(func())) {
	previousFiles, previousRecords := filesByHashCache, prevRecordCache
	filesByHashCache = cache.NewLRUCache[Files](&cache.CacheConfig{Name: "test:torrent_stream:files"})
	prevRecordCache = cache.NewLRUCache[prevRecordData](&cache.CacheConfig{Name: "test:torrent_stream:records"})
	cleanup(func() {
		filesByHashCache, prevRecordCache = previousFiles, previousRecords
	})
}

// ObserveFileRecordingForTesting primes an empty read-cache entry and observes
// both the write-cache updates and final read-cache invalidation. Serial fixtures
// can wait for this before restoring caches used by an asynchronous upsert.
func ObserveFileRecordingForTesting(hash string, paths []string) func() bool {
	readCache, writeCache := filesByHashCache, prevRecordCache
	readCache.Add(hash, Files{})
	return func() bool {
		for _, path := range paths {
			if !writeCache.Has(hash + ":" + path) {
				return false
			}
		}
		return !readCache.Has(hash)
	}
}
