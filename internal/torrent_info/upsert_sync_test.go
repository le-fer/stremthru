package torrent_info

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/cache"
	"github.com/MunifTanjim/stremthru/internal/db"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) {
	t.Helper()

	previousRecords := prevRecordCache
	prevRecordCache = cache.NewLRUCache[prevRecordData](&cache.CacheConfig{Name: "test:torrent_info:records"})
	t.Cleanup(func() { prevRecordCache = previousRecords })
	ts.ResetCachesForTesting(t.Cleanup)

	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	// A ":memory:" database is per-connection; one pooled connection keeps
	// schema/data visible across queries.
	sqlDB.SetMaxOpenConns(1)

	// Apply the real production migrations (dir is relative to this package).
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, "../../migrations/sqlite"))

	db.SetForTesting(sqlDB, t.Cleanup)
}

func queryStreamSId(t *testing.T, hash, path string) (string, error) {
	t.Helper()
	var sid string
	err := db.QueryRow("SELECT sid FROM torrent_stream WHERE h = ? AND p = ?", hash, path).Scan(&sid)
	return sid, err
}

// Upsert must record the torrent_stream row before returning, else the later
// TagStremId UPDATE races it and silently matches zero rows.
func TestUpsertRecordsTorrentStreamBeforeReturning(t *testing.T) {
	setupTestDB(t)

	hash := strings.Repeat("a", 40)
	path := "/Some.Movie.2024.1080p.mkv"

	err := Upsert([]TorrentInfoInsertData{{
		Hash:         hash,
		TorrentTitle: "Some Movie 2024 1080p",
		Size:         1234,
		Source:       TorrentInfoSourceTorrentio,
		Files: ts.Files{{
			Idx:  0,
			Path: path,
			Name: "Some.Movie.2024.1080p.mkv",
			Size: 1234,
		}},
	}}, TorrentInfoCategoryMovie, true)
	require.NoError(t, err)

	// Synchrony guard: the row must exist the instant Upsert returns.
	sid, err := queryStreamSId(t, hash, path)
	require.NoError(t, err, "stream row missing immediately after Upsert returned")
	assert.Equal(t, "*", sid, "freshly recorded stream should be untagged")

	ts.TagStremId(hash, path, "tt1234567")

	sid, err = queryStreamSId(t, hash, path)
	require.NoError(t, err)
	assert.Equal(t, "tt1234567", sid, "TagStremId did not tag the recorded stream")
}

func TestUpsertCanonicalizesAndValidatesHashes(t *testing.T) {
	setupTestDB(t)
	hash := strings.Repeat("a", 39) + "b"
	items := []TorrentInfoInsertData{
		{Hash: strings.ToUpper(hash), TorrentTitle: "Canonical", Source: TorrentInfoSourceTorrentio},
		{Hash: strings.Repeat("z", 40), TorrentTitle: "Invalid", Source: TorrentInfoSourceTorrentio},
		{Hash: strings.Repeat("a", 64), TorrentTitle: "Unsupported v2", Source: TorrentInfoSourceTorrentio},
	}
	require.NoError(t, Upsert(items, TorrentInfoCategoryMovie, true))

	var canonical, uppercase, invalid, unsupported int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_info WHERE hash = ?", hash).Scan(&canonical))
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_info WHERE hash = ?", strings.ToUpper(hash)).Scan(&uppercase))
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_info WHERE hash = ?", strings.Repeat("z", 40)).Scan(&invalid))
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM torrent_info WHERE hash = ?", strings.Repeat("a", 64)).Scan(&unsupported))
	require.Equal(t, 1, canonical)
	require.Zero(t, uppercase)
	require.Zero(t, invalid)
	require.Zero(t, unsupported)
}
