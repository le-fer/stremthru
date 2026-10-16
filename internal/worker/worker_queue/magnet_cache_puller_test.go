package worker_queue

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMagnetCachePullerQueueDeduplicatesHashCase(t *testing.T) {
	q := WorkerQueue[MagnetCachePullerQueueItem]{getKey: MagnetCachePullerQueue.getKey, getGroupKey: MagnetCachePullerQueue.getGroupKey, transform: MagnetCachePullerQueue.transform}
	hash := strings.Repeat("ab", 20)
	item := MagnetCachePullerQueueItem{Hash: strings.ToUpper(hash), StoreCode: "tb", SId: "tt1234567", StoreToken: "CaseSensitiveToken"}
	q.Queue(item)
	item.Hash = hash
	q.Queue(item)
	count := 0
	q.m.Range(func(k, v any) bool {
		count++
		stored := v.(WorkerQueueItem[MagnetCachePullerQueueItem]).v
		require.Equal(t, hash, stored.Hash)
		require.Equal(t, "CaseSensitiveToken", stored.StoreToken)
		return true
	})
	require.Equal(t, 1, count)
}
