package torrent_stream

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

type delayedFileSnapshotExecutor struct {
	db.Executor
	hash, files       string
	snapshot, updated chan struct{}
}

func (e delayedFileSnapshotExecutor) Query(query string, args ...any) (*sql.Rows, error) {
	// Bound rows model a primary snapshot delayed until after a concurrent write.
	rows, err := e.Executor.Query("SELECT ? AS h, ? AS files", e.hash, e.files)
	close(e.snapshot)
	<-e.updated
	return rows, err
}
func setupFileCacheTestDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite3", "file:"+uuid.NewString()+"?mode=memory&cache=shared")
	require.NoError(t, err)
	db.SetForTesting(conn, t.Cleanup)
	conn.SetMaxOpenConns(2)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.Up(conn, "../../migrations/sqlite"))
	return conn
}

func TestPrimaryFileReadsDoNotPublishDelayedSnapshots(t *testing.T) {
	setupFileCacheTestDB(t)
	hash := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
	t.Cleanup(func() { filesByHashCache.Remove(hash) })
	old := Files{{Path: "/video.mkv", Name: "video.mkv", Source: "tio", SId: "*"}}
	require.NoError(t, RecordFromPeer([]InsertData{{Hash: hash, File: old[0]}}, false))
	oldJSON, err := json.Marshal(old)
	require.NoError(t, err)
	ex := delayedFileSnapshotExecutor{Executor: db.GetPrimary(), hash: hash, files: string(oldJSON), snapshot: make(chan struct{}), updated: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-ex.updated:
		default:
			close(ex.updated)
		}
	})
	done := make(chan error, 1)
	go func() { _, err := getFilesByHashes([]string{hash}, ex, false); done <- err }()
	<-ex.snapshot
	require.NoError(t, RecordFromPeer([]InsertData{{Hash: hash, File: File{Path: "/video.mkv", Name: "video.mkv", Source: "tio", SId: "tt1234567:1:2"}}}, false))
	// Prime the ordinary cache with the committed write while the old primary
	// snapshot is still in flight. Its eventual result must not replace this cache.
	current, err := GetFilesByHashes([]string{hash})
	require.NoError(t, err)
	require.Equal(t, "tt1234567:1:2", current[hash][0].SId)
	close(ex.updated)
	require.NoError(t, <-done)
	var cached Files
	require.True(t, filesByHashCache.Get(hash, &cached))
	require.Equal(t, "tt1234567:1:2", cached[0].SId, "primary reads must not publish snapshots into the shared cache")
	fresh, err := GetFilesByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.Equal(t, "tt1234567:1:2", fresh[hash][0].SId)
}

func TestPrimaryFileReadsBypassStaleSharedCache(t *testing.T) {
	conn := setupFileCacheTestDB(t)
	hash := strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678"
	t.Cleanup(func() { filesByHashCache.Remove(hash) })
	_, err := conn.Exec("INSERT INTO torrent_stream (h,p,sid,src) VALUES (?, ?, ?, ?)", hash, "/video.mkv", "tt1234567:1:1", "tio")
	require.NoError(t, err)
	before, err := GetFilesByHashes([]string{hash})
	require.NoError(t, err)
	require.Equal(t, "tt1234567:1:1", before[hash][0].SId)
	_, err = conn.Exec("UPDATE torrent_stream SET sid = ? WHERE h = ?", "tt1234567:1:2", hash)
	require.NoError(t, err)
	hit, miss := GetReadCacheStats()
	ordinary, err := GetFilesByHashes([]string{hash})
	require.NoError(t, err)
	require.Equal(t, "tt1234567:1:1", ordinary[hash][0].SId, "ordinary reads retain their existing cache behavior")
	nextHit, nextMiss := GetReadCacheStats()
	require.Equal(t, hit+1, nextHit)
	require.Equal(t, miss, nextMiss)
	primary, err := GetFilesByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.Equal(t, "tt1234567:1:2", primary[hash][0].SId)
	primaryHit, primaryMiss := GetReadCacheStats()
	require.Equal(t, nextHit, primaryHit, "cache bypasses must not count as cache hits")
	require.Equal(t, nextMiss, primaryMiss, "cache bypasses must not count as cache misses")
	var cached Files
	require.True(t, filesByHashCache.Get(hash, &cached))
	require.Equal(t, "tt1234567:1:1", cached[0].SId, "primary reads must not populate the ordinary cache")
	// Repeat primary reads must observe direct SQL writes even without any cache
	// deletion, including writes by older instances or during Redis failures.
	_, err = conn.Exec("UPDATE torrent_stream SET sid = ? WHERE h = ?", "tt1234567:1:3", hash)
	require.NoError(t, err)
	primary, err = GetFilesByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.Equal(t, "tt1234567:1:3", primary[hash][0].SId)
	_, err = conn.Exec("DELETE FROM torrent_stream WHERE h = ?", hash)
	require.NoError(t, err)
	deleted, err := GetFilesByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.NotContains(t, deleted, hash)
}
