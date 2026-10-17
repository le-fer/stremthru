package torrent_info

import (
	"hash/fnv"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/db"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/stretchr/testify/require"
)

func seedPeerIMDbTitle(t *testing.T, sid, title, original string, year int) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO imdb_title (tid, title, orig_title, year, type, is_adult)
 VALUES (?, ?, ?, ?, 'tvSeries', ?)
 ON CONFLICT (tid) DO UPDATE SET title = EXCLUDED.title, orig_title = EXCLUDED.orig_title, year = EXCLUDED.year`, sid, title, original, year, false)
	require.NoError(t, err)
}

func setupPeerImportTestDB(t *testing.T) {
	t.Helper()
	setupTestDB(t)
	seedPeerIMDbTitle(t, "tt1234567", "Diagnostic Programme", "", 0)
}

func TestPeerFallbackRequiresIMDbIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, canonical, original, release string
		year                               int
		wantMapping                        bool
	}{
		{name: "canonical", canonical: "Diagnostic Programme", release: "Diagnostic.Programme.S01E02", wantMapping: true},
		{name: "original title", canonical: "Translated Series", original: "Diagnostic Programme", release: "Diagnostic.Programme.S01E02", wantMapping: true},
		{name: "punctuation and case", canonical: "DIAGNOSTIC: Programme", release: "Diagnostic.Programme.S01.Complete", wantMapping: true},
		{name: "composed canonical title", canonical: "Café Diagnostic", release: "Cafe\u0301.Diagnostic.S01E02", wantMapping: true},
		{name: "decomposed canonical title", canonical: "Cafe\u0301 Diagnostic", release: "Café.Diagnostic.S01E02", wantMapping: true},
		{name: "complete pack", canonical: "Diagnostic Programme", release: "Diagnostic.Programme.Complete.1080p", wantMapping: true},
		{name: "different title", canonical: "Diagnostic Programme", release: "Unrelated.Collection.Complete.1080p"},
		{name: "unknown IMDb title", release: "Diagnostic.Programme.S01E02"},
		{name: "different year", canonical: "Diagnostic Programme", year: 2024, release: "Diagnostic.Programme.2023.S01E02"},
		{name: "matching year", canonical: "Diagnostic Programme", year: 2024, release: "Diagnostic.Programme.2024.S01E02", wantMapping: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupPeerMappingTestDB(t)
			const sid = "tt1234567"
			if tc.canonical != "" {
				seedPeerIMDbTitle(t, sid, tc.canonical, tc.original, tc.year)
			}
			hash := peerTestHash()
			items := []TorrentInfoInsertData{{Hash: hash, TorrentTitle: tc.release, Source: TorrentInfoSourceTorrentio}}
			for range 2 {
				err := UpsertFromPeer(items, sid)
				if tc.canonical == "" && tc.original == "" {
					require.ErrorContains(t, err, "IMDb title metadata unavailable")
				} else {
					require.NoError(t, err)
				}
				var count int
				require.NoError(t, db.GetPrimary().QueryRow("SELECT COUNT(*) FROM imdb_torrent WHERE hash = ? AND tid = ?", hash, sid).Scan(&count))
				if tc.wantMapping {
					require.Equal(t, 1, count)
				} else {
					require.Zero(t, count)
				}
				stored, err := GetByHashesFromPrimary([]string{hash})
				require.NoError(t, err)
				require.True(t, stored[hash].IsParsed(), "unmatched imports remain stored and parsed")
			}
		})
	}
}

func TestPeerImportDoesNotPromoteCollectionFileMapping(t *testing.T) {
	setupPeerMappingTestDB(t)
	const sid = "tt1234567"
	seedPeerIMDbTitle(t, sid, "Diagnostic Programme", "", 2020)
	hash := peerTestHash()
	items := []TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Unrelated.Collection.Complete.1080p", Source: TorrentInfoSourceTorrentio,
		Files: ts.Files{
			{Path: "/Extras/Diagnostic Programme Special.mkv", Source: "tb", SId: sid + ":0:1"},
			{Path: "/Unrelated Programme/Unrelated Programme - Episode 3.mkv", Source: "tb"},
		},
	}}
	for range 2 {
		require.NoError(t, UpsertFromPeer(items, sid))
		var count int
		require.NoError(t, db.GetPrimary().QueryRow("SELECT COUNT(*) FROM imdb_torrent WHERE hash = ?", hash).Scan(&count))
		require.Zero(t, count, "a file relationship must not promote an unrelated collection to the requested series")
		hashes, err := ListHashesByStremIdFromPrimary(sid + ":2:3")
		require.NoError(t, err)
		require.NotContains(t, hashes, hash)
		special, err := ListHashesByStremIdFromPrimary(sid + ":0:1")
		require.NoError(t, err)
		require.Contains(t, special, hash, "existing explicit file relationships are preserved")
	}
}

func TestPeerFallbackRechecksParsedIdentityAfterLockWait(t *testing.T) {
	if db.Dialect != db.DBDialectPostgres {
		t.Skip("requires concurrent PostgreSQL writers")
	}
	conn := setupPeerMappingTestDB(t)
	seedPeerIMDbTitle(t, "tt1234567", "Diagnostic Programme", "", 0)
	hash := peerTestHash()
	// Block mapping acquisition without holding any metadata row lock. The
	// importer can finish parsing and checking identity before it waits here.
	h := fnv.New32a()
	h.Write([]byte(hash))
	key := int32(h.Sum32() % 256)
	const namespace = int32(0x746d6170)
	blocker, err := conn.Begin()
	require.NoError(t, err)
	defer blocker.Rollback()
	_, err = blocker.Exec("SELECT pg_advisory_xact_lock($1, $2)", namespace, key)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- UpsertFromPeer([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}, "tt1234567")
	}()
	// Always join the writer before the test restores the shared database.
	joined := false
	defer func() {
		blocker.Rollback()
		if !joined {
			require.NoError(t, <-done)
		}
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := db.GetPrimary().QueryRow("SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND classid = ? AND objid = ? AND NOT granted)", namespace, key).Scan(&waiting)
		return err == nil && waiting
	}, 5*time.Second, 10*time.Millisecond)
	// Forced reprocessing can change parsed identity without changing raw title.
	_, err = db.Exec("UPDATE torrent_info SET title = ? WHERE hash = ?", "Unrelated Programme", hash)
	require.NoError(t, err)
	require.NoError(t, blocker.Commit())
	err = <-done
	joined = true
	require.NoError(t, err)
	var count int
	require.NoError(t, db.GetPrimary().QueryRow("SELECT COUNT(*) FROM imdb_torrent WHERE hash = ?", hash).Scan(&count))
	require.Zero(t, count, "identity verified before a lock wait must still match at insertion")
}
