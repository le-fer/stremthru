package store_util

import (
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestMergeCheckMagnetItemsPreservesEvidenceAndCachedStatus(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	decoded, err := hex.DecodeString(hash)
	require.NoError(t, err)
	base32Hash := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(decoded)
	other := strings.Repeat("cd", 20)
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "miss first", true: "cached first"}[reverse], func(t *testing.T) {
			// Spare capacity catches accidental append into caller-owned evidence.
			firstFiles := make([]store.MagnetFile, 1, 4)
			firstFiles[0] = store.MagnetFile{Name: "movie.mkv", Path: "/setup.exe", Source: "peer"}
			items := []store.CheckMagnetDataItem{
				{Hash: strings.ToUpper(hash), Name: "Miss", Status: store.MagnetStatusUnknown, Files: firstFiles},
				{Hash: other, Status: store.MagnetStatusUnknown, Files: []store.MagnetFile{}},
				{Hash: base32Hash, Name: "Cached", Magnet: "fixture-magnet", Size: 123, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "setup.exe", Path: "/movie.mkv"}, {Name: "movie.mkv", Path: "/movie.mkv"}}},
			}
			if reverse {
				items[0], items[2] = items[2], items[0]
			}
			merged := MergeCheckMagnetItems(items)
			require.Len(t, merged, 2)
			require.Equal(t, hash, merged[0].Hash)
			require.Equal(t, store.MagnetStatusCached, merged[0].Status)
			require.Equal(t, "Cached", merged[0].Name)
			require.Equal(t, "fixture-magnet", merged[0].Magnet)
			require.EqualValues(t, 123, merged[0].Size)
			require.Len(t, merged[0].Files, 3)
			require.ElementsMatch(t, append(append([]store.MagnetFile{}, items[0].Files...), items[2].Files...), merged[0].Files)
			require.Equal(t, other, merged[1].Hash)
			require.NotNil(t, merged[1].Files, "preserve supplied empty-list representation")
			merged[0].Files[0].Name = "changed output"
			require.NotEqual(t, "changed output", items[0].Files[0].Name)
			require.Len(t, firstFiles, 1)
			require.Equal(t, store.MagnetFile{}, firstFiles[:cap(firstFiles)][1], "merging must not write into the input's spare capacity")
		})
	}
}

func TestMergeCheckMagnetItemsDoesNotInventCachedStatus(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	items := []store.CheckMagnetDataItem{
		{Hash: hash, Status: store.MagnetStatusUnknown, Files: []store.MagnetFile{{Name: "readme.txt"}}},
		{Hash: strings.ToUpper(hash), Status: store.MagnetStatusDownloaded, Files: []store.MagnetFile{{Name: "movie.mkv"}}},
		{Hash: "", Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv"}}},
	}
	merged := MergeCheckMagnetItems(items)
	require.Len(t, merged, 1)
	require.Equal(t, store.MagnetStatusUnknown, merged[0].Status)
	require.Len(t, merged[0].Files, 2)
}

func TestMergeCheckMagnetItemsManySiblingsPreserveOrderAndOwnership(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	items := make([]store.CheckMagnetDataItem, 40)
	for i := range items {
		files := make([]store.MagnetFile, 1, 64)
		files[0].Name = fmt.Sprintf("video-%d.mkv", i)
		items[i] = store.CheckMagnetDataItem{Hash: hash, Name: fmt.Sprintf("torrent-%d", i), Status: store.MagnetStatusUnknown, Files: files}
	}
	items[20].Hash = strings.ToUpper(hash)
	items[20].Status = store.MagnetStatusCached
	items[39].Files[0].Name = "setup.exe"
	merged := MergeCheckMagnetItems(items)
	require.Len(t, merged, 1)
	require.Equal(t, "torrent-20", merged[0].Name)
	require.Equal(t, store.MagnetStatusCached, merged[0].Status)
	require.Len(t, merged[0].Files, len(items))
	for i := range items {
		require.Equal(t, items[i].Files[0], merged[0].Files[i])
		merged[0].Files[i].Name = "changed output"
		require.NotEqual(t, "changed output", items[i].Files[0].Name)
		for _, spare := range items[i].Files[1:cap(items[i].Files)] {
			require.Equal(t, store.MagnetFile{}, spare)
		}
	}
}

func BenchmarkMergeCheckMagnetItemsDuplicates(b *testing.B) {
	for _, count := range []int{10, 40} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			files := make([]store.MagnetFile, 100)
			for i := range files {
				files[i].Name = fmt.Sprintf("video-%d.mkv", i)
			}
			items := make([]store.CheckMagnetDataItem, count)
			for i := range items {
				items[i] = store.CheckMagnetDataItem{Hash: strings.Repeat("a", 40), Status: store.MagnetStatusCached, Files: files}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				merged := MergeCheckMagnetItems(items)
				if len(merged) != 1 || len(merged[0].Files) != count*100 {
					b.Fatal("lost duplicate file evidence")
				}
			}
		})
	}
}

func TestPartitionCheckMagnetItems(t *testing.T) {
	hashWithFiles := strings.Repeat("ab", 20)
	hashWithoutFiles := strings.Repeat("cd", 20)
	items := []store.CheckMagnetDataItem{
		{Hash: hashWithFiles, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv"}}},
		{Hash: hashWithoutFiles, Status: store.MagnetStatusCached},
	}

	partition := PartitionCheckMagnetItems(PartitionCheckMagnetItemsParams{
		Items:                   items,
		RecheckMissingFileLists: true,
	})
	require.True(t, partition.Rechecking)
	require.Contains(t, partition.Found, hashWithFiles)
	require.NotContains(t, partition.Found, hashWithoutFiles)
	require.Contains(t, partition.Reusable, hashWithFiles)
	require.Contains(t, partition.Reusable, hashWithoutFiles)

	partition = PartitionCheckMagnetItems(PartitionCheckMagnetItemsParams{
		Items:                   items,
		RecheckMissingFileLists: true,
		LocalOnly:               true,
	})
	require.False(t, partition.Rechecking)
	require.Contains(t, partition.Found, hashWithFiles)
	require.Contains(t, partition.Found, hashWithoutFiles)
}

func TestCheckMagnetItemsPartitionRestore(t *testing.T) {
	failure := errors.New("recheck failed")
	cached := store.CheckMagnetDataItem{Hash: "cached", Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv"}}}
	fileless := store.CheckMagnetDataItem{Hash: "fileless", Status: store.MagnetStatusCached}
	partition := CheckMagnetItemsPartition{
		Reusable:   map[string]store.CheckMagnetDataItem{"cached": cached, "fileless": fileless},
		Rechecking: true,
	}
	data, err := partition.Restore([]string{"fileless", "missing", "cached"}, failure)
	require.NoError(t, err)
	require.ErrorIs(t, data.RecheckError, failure)
	require.Equal(t, []store.CheckMagnetDataItem{fileless, {Hash: "missing", Magnet: "magnet:?xt=urn:btih:missing", Status: store.MagnetStatusUnknown, Files: []store.MagnetFile{}}, cached}, data.Items)
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "recheck failed")
	require.Contains(t, string(encoded), `"recheck_failed":true`)
	data, err = (CheckMagnetItemsPartition{}).Restore(nil, failure)
	require.Nil(t, data)
	require.ErrorIs(t, err, failure)
}

func TestPartitionCheckMagnetItemsRetainsPeerRecheckFailure(t *testing.T) {
	returned, missing := strings.Repeat("ab", 20), strings.Repeat("cd", 20)
	peerFailure := errors.New("peer recheck failure")
	lookupFailure := errors.New("direct lookup failure")
	for _, localOnly := range []bool{false, true} {
		partition := PartitionCheckMagnetItems(PartitionCheckMagnetItemsParams{
			Items:                   []store.CheckMagnetDataItem{{Hash: returned, Status: store.MagnetStatusCached}},
			RecheckMissingFileLists: true,
			LocalOnly:               localOnly,
			RecheckError:            peerFailure,
		})
		require.False(t, partition.Rechecking, "do not retry already attempted fileless answers")
		require.Contains(t, partition.Found, returned)
		require.NotContains(t, partition.Found, missing, "unrelated omissions still need direct fallback")
		require.ErrorIs(t, partition.RecheckError, peerFailure)
		data, err := partition.Restore([]string{returned, missing}, lookupFailure)
		require.NoError(t, err)
		require.ErrorIs(t, data.RecheckError, peerFailure)
		require.ErrorIs(t, data.RecheckError, lookupFailure)
		require.Equal(t, store.MagnetStatusCached, data.Items[0].Status)
		require.Equal(t, store.MagnetStatusUnknown, data.Items[1].Status)
	}
}

func TestFileListRecheckAttributionCountsEachSubmittedHashOnce(t *testing.T) {
	fileless, normal, known := strings.Repeat("ab", 20), strings.Repeat("cd", 20), strings.Repeat("ef", 20)
	partition := PartitionCheckMagnetItems(PartitionCheckMagnetItemsParams{
		Items: []store.CheckMagnetDataItem{
			{Hash: fileless, Status: store.MagnetStatusCached},
			{Hash: known, Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv"}}},
		},
		RecheckMissingFileLists: true,
	})
	require.Equal(t, 1, partition.FileListRecheckCount([]string{fileless, normal}, nil))
	require.Equal(t, 1, partition.FileListRecheckCount([]string{fileless, normal}, map[string]struct{}{fileless: {}}), "same-provider and cross-store attribution must not double-count a hash")
	require.Equal(t, 2, partition.FileListRecheckCount([]string{fileless, normal}, map[string]struct{}{normal: {}}))
	require.False(t, partition.IsFileListRecheck(known, nil))
}
