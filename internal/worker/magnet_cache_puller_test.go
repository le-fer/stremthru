package worker

import (
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/logger"
	"github.com/MunifTanjim/stremthru/internal/magnet_cache"
	"github.com/MunifTanjim/stremthru/internal/testutil"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestRecordMagnetCachePullResultRetainsNoncachedEvidence(t *testing.T) {
	testutil.SetupDB(t)
	cachedHash := strings.Repeat("ab", 20)
	missHash := strings.Repeat("cd", 20)
	recordMagnetCachePullResult(store.StoreCodeTorBox, []store.CheckMagnetDataItem{
		{Hash: strings.ToUpper(cachedHash), Status: store.MagnetStatusCached, Files: []store.MagnetFile{{Name: "movie.mkv", Path: "/movie.mkv"}}},
		{Hash: strings.ToUpper(missHash), Status: store.MagnetStatusUnknown, Files: []store.MagnetFile{{Name: "setup.exe", Path: "/setup.exe"}}},
	}, logger.Scoped("test"))

	rows, err := magnet_cache.GetByHashes(store.StoreCodeTorBox, []string{cachedHash, missHash}, "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	statusByHash := map[string]bool{}
	for _, row := range rows {
		statusByHash[row.Hash] = row.IsCached
	}
	require.True(t, statusByHash[cachedHash])
	_, promoted := statusByHash[missHash]
	require.False(t, promoted, "noncached file evidence must not create a cache entry")

	filesByHash, err := torrent_stream.GetFilesByHashes([]string{cachedHash, missHash})
	require.NoError(t, err)
	require.True(t, filesByHash[cachedHash].HasAllowedVideo())
	require.True(t, filesByHash[missHash].HasDisallowedFile())
}

func TestCollectMagnetCacheFilesPreservesPathAndNameEvidence(t *testing.T) {
	input := []store.MagnetFile{
		{Idx: 1, Name: "movie.mkv", Path: "/movie.mkv", Size: 100, Source: "rd", VideoHash: "fixture-video-hash"},
		{Idx: 2, Name: "movie.mkv", Path: "/setup.exe", Size: 1, Source: "rd"},
		{Idx: 3, Name: "setup.exe", Path: "/movie.mkv", Size: 1, Source: "rd"},
	}
	input = append(input, input[0])
	files := collectMagnetCacheFiles("fixture-hash", input, logger.Scoped("test"))
	require.Len(t, files, 3, "only an identical path/name pair should be deduplicated")
	for i, file := range files {
		require.Equal(t, input[i].Name, file.Name)
		require.Equal(t, input[i].Path, file.Path)
		require.Equal(t, input[i].Idx, file.Idx)
		require.Equal(t, input[i].Size, file.Size)
		require.Equal(t, input[i].Source, file.Source)
		require.Equal(t, input[i].VideoHash, file.VideoHash)
	}
	require.True(t, files.HasDisallowedFile())
}
