package stats

import (
	"sync"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestRecordWithInfoDoesNotDoubleCount(t *testing.T) {
	name := store.StoreName(t.Name())
	t.Cleanup(func() {
		globalMu.Lock()
		delete(globalStats, name)
		globalMu.Unlock()
	})
	Record(name, "check", time.Millisecond, false)
	RecordWithInfo(name, "check", time.Millisecond, true, CheckCall(1, 1))
	for _, entry := range GetSnapshot(time.Hour) {
		if entry.Name == name {
			require.Len(t, entry.Methods, 1)
			require.EqualValues(t, 2, entry.Methods[0].TotalCount)
			require.EqualValues(t, 1, entry.Methods[0].ErrorCount)
			require.Equal(t, RecheckStats{OnlyCalls: 1, Errors: 1, Hashes: 1}, entry.Methods[0].Rechecks)
			return
		}
	}
	t.Fatal("recorded stats missing from snapshot")
}

func TestRecheckSubsetsShareSingleCallRecord(t *testing.T) {
	ms := &MethodStats{}
	ms.Record(time.Millisecond, false)
	ms.record(time.Millisecond, false, CheckCall(2, 2))
	ms.record(time.Millisecond, true, CheckCall(3, 5))
	// A file-fetch follow-up is a second API call, not another hash submission.
	ms.record(time.Millisecond, true, CallInfo{Kind: CallRecheckOnly})
	snap := ms.Snapshot(time.Hour)
	require.EqualValues(t, 4, snap.TotalCount)
	require.EqualValues(t, 2, snap.ErrorCount)
	require.Equal(t, RecheckStats{OnlyCalls: 2, MixedCalls: 1, Errors: 2, Hashes: 5}, snap.Rechecks)
	require.Equal(t, 50.0, snap.ErrorRate)
}

func TestRecheckBucketsExpireAndReset(t *testing.T) {
	ms := &MethodStats{}
	old := ms.getBucket(time.Now().Add(-2 * time.Hour))
	old.count = 100
	old.rechecks = RecheckStats{OnlyCalls: 100, Hashes: 100}
	require.Zero(t, ms.Snapshot(time.Hour).TotalCount)
	ms.Record(time.Millisecond, false)
	snap := ms.Snapshot(time.Hour)
	require.EqualValues(t, 1, snap.TotalCount)
	require.Equal(t, RecheckStats{}, snap.Rechecks)
}

func TestConcurrentRecheckCounting(t *testing.T) {
	ms := &MethodStats{}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 100 {
				ms.record(time.Millisecond, true, CheckCall(2, 3))
			}
		})
	}
	wg.Wait()
	snap := ms.Snapshot(time.Hour)
	require.EqualValues(t, 1000, snap.TotalCount)
	require.EqualValues(t, 1000, snap.ErrorCount)
	require.Equal(t, RecheckStats{MixedCalls: 1000, Errors: 1000, Hashes: 2000}, snap.Rechecks)
}
