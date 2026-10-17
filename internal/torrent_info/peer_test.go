package torrent_info

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_torrent"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func peerTestHash() string { return strings.ReplaceAll(uuid.NewString(), "-", "") + "12345678" }

func TestUpsertFromPeerIMDb(t *testing.T) {
	setupPeerImportTestDB(t)
	sid := "tt1234567"
	var items []TorrentInfoInsertData
	for _, title := range []string{"Diagnostic.Programme.S01E02.1080p", "Diagnostic.Programme.S01E03.1080p", "Diagnostic.Programme.S01.1080p", "Diagnostic.Programme.S02.1080p", "Diagnostic.Programme.S01E02E04.1080p"} {
		items = append(items, TorrentInfoInsertData{Hash: peerTestHash(), TorrentTitle: title, Source: TorrentInfoSourceTorrentio})
	}
	require.NoError(t, UpsertFromPeer(items, sid))
	hashes, err := ListHashesByStremId(sid + ":1:2")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{items[0].Hash, items[2].Hash, items[4].Hash}, hashes)
	hashes, err = ListHashesByStremId(sid + ":1:3")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{items[1].Hash, items[2].Hash, items[4].Hash}, hashes)
	// Idempotent with the upsert cache hot, and no background workers.
	require.NoError(t, UpsertFromPeer(items, sid))
	hashes, err = ListHashesByStremId(sid + ":1:2")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{items[0].Hash, items[2].Hash, items[4].Hash}, hashes)
}

func TestUpsertFromPeerPreservesMappingsAndStoredTitle(t *testing.T) {
	setupPeerImportTestDB(t)
	for _, kind := range []string{"mapped", "file", "stored-title", "empty-mapping", "movie"} {
		t.Run(kind, func(t *testing.T) {
			hash := peerTestHash()
			original := TorrentInfoInsertData{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02.1080p", Source: TorrentInfoSourceDHT}
			if kind == "movie" {
				original.TorrentTitle = "Diagnostic.Movie.2024.1080p"
				seedPeerIMDbTitle(t, "tt1234567", "Diagnostic Movie", "", 2024)
			}
			require.NoError(t, Upsert([]TorrentInfoInsertData{original}, "", false))
			switch kind {
			case "mapped":
				require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{Hash: hash, TId: "tt7654321"}}))
			case "empty-mapping":
				require.NoError(t, imdb_torrent.Insert([]imdb_torrent.IMDBTorrent{{Hash: hash}}))
			case "file":
				require.NoError(t, ts.Record([]ts.InsertData{{Hash: hash, File: ts.File{Path: "/video.mkv", SId: "tt7654321:1:2", Source: "dht"}}}, false))
			}
			title := "Diagnostic.Programme.S01E03.1080p"
			if kind == "movie" {
				title = "Diagnostic.Movie.2024.1080p"
			}
			require.NoError(t, UpsertFromPeer([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: title, Source: TorrentInfoSourceTorrentio}}, "tt1234567"))
			hashes, err := ListHashesByStremId("tt1234567")
			require.NoError(t, err)
			if kind == "mapped" || kind == "file" {
				assert.NotContains(t, hashes, hash)
			} else {
				assert.Contains(t, hashes, hash)
			}
			info, err := GetByHash(hash)
			require.NoError(t, err)
			assert.True(t, info.IsParsed())
			assert.Equal(t, original.TorrentTitle, info.ParserInput)
		})
	}
}

func TestUpsertFromPeerPersistenceFailure(t *testing.T) {
	setupPeerImportTestDB(t)
	hash := peerTestHash()
	_, err := db.Exec(`CREATE TRIGGER fail_peer_mapping BEFORE INSERT ON imdb_torrent BEGIN SELECT RAISE(FAIL, 'mapping failed'); END`)
	require.NoError(t, err)
	err = UpsertFromPeer([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}, "tt1234567")
	require.ErrorContains(t, err, "mapping failed")
	// Metadata may be committed before failure, but the import must report it.
	_, err = db.Exec("DROP TRIGGER fail_peer_mapping")
	require.NoError(t, err)
	require.NoError(t, UpsertFromPeer([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}, "tt1234567"))
	hashes, err := ListHashesByStremId("tt1234567:1:2")
	require.NoError(t, err)
	assert.Equal(t, []string{hash}, hashes)
}

func TestPeerImportPreservesExplicitFileMappings(t *testing.T) {
	setupPeerImportTestDB(t)
	for _, existingSID := range []string{"tt7654321:1:2", "*"} {
		t.Run(existingSID, func(t *testing.T) {
			hash := peerTestHash()
			require.NoError(t, Upsert([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceRealDebrid,
				Files: ts.Files{{Path: "/video.mkv", Name: "video.mkv", Source: "rd", SId: existingSID, ASId: "999:2"}},
			}}, "", false))
			imported := []TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio,
				Files: ts.Files{{Path: "/video.mkv", Name: "video.mkv", Source: "tio", SId: "tt1234567:1:2", ASId: "123:2"}},
			}}
			require.NoError(t, UpsertFromPeer(imported, "tt1234567"))
			require.NoError(t, UpsertFromPeer(imported, "tt1234567"))
			var sid, asid string
			require.NoError(t, db.GetPrimary().QueryRow("SELECT sid, asid FROM torrent_stream WHERE h = ? AND p = ?", hash, "/video.mkv").Scan(&sid, &asid))
			if existingSID == "*" {
				assert.Equal(t, "tt1234567:1:2", sid)
			} else {
				assert.Equal(t, existingSID, sid)
			}
			assert.Equal(t, "999:2", asid)
			candidates, err := ListHashesByStremIdFromPrimary("tt1234567:1:2")
			require.NoError(t, err)
			if existingSID == "*" {
				assert.Contains(t, candidates, hash)
			} else {
				assert.NotContains(t, candidates, hash)
			}
		})
	}
}

func TestPeerImportFillsMissingFileMappingsWithSameSource(t *testing.T) {
	setupPeerImportTestDB(t)
	hash := peerTestHash()
	items := []TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio,
		Files: ts.Files{{Path: "/video.mkv", Name: "video.mkv", Source: "tio"}},
	}}
	require.NoError(t, Upsert(items, "", false))
	items[0].Files[0].SId = "tt1234567:1:2"
	items[0].Files[0].ASId = "123:2"
	require.NoError(t, UpsertFromPeer(items, "tt1234567"))
	var sid, asid string
	require.NoError(t, db.GetPrimary().QueryRow("SELECT sid, asid FROM torrent_stream WHERE h = ? AND p = ?", hash, "/video.mkv").Scan(&sid, &asid))
	assert.Equal(t, "tt1234567:1:2", sid)
	assert.Equal(t, "123:2", asid)
	// An authoritative writer must still be able to change an explicit mapping.
	require.NoError(t, ts.Record([]ts.InsertData{{Hash: hash, File: ts.File{Path: "/video.mkv", Source: "rd", SId: "tt7654321:1:2", ASId: "999:2"}}}, false))
	require.NoError(t, db.GetPrimary().QueryRow("SELECT sid, asid FROM torrent_stream WHERE h = ? AND p = ?", hash, "/video.mkv").Scan(&sid, &asid))
	assert.Equal(t, "tt7654321:1:2", sid)
	assert.Equal(t, "999:2", asid)
}

func TestPeerImportCompleteSeriesPack(t *testing.T) {
	setupPeerImportTestDB(t)
	var items []TorrentInfoInsertData
	for _, title := range []string{"Diagnostic.Programme.Complete.1080p", "Diagnostic.Programme.S01.Complete.1080p", "Diagnostic.Programme.S02.Complete.1080p", "Diagnostic.Programme.1080p", "Diagnostic.Programme.S01E03"} {
		items = append(items, TorrentInfoInsertData{Hash: peerTestHash(), TorrentTitle: title, Source: TorrentInfoSourceTorrentio})
	}
	require.NoError(t, UpsertFromPeer(items, "tt1234567"))
	for _, sid := range []string{"tt1234567:1:2", "tt1234567:2:2"} {
		hashes, err := ListHashesByStremId(sid)
		require.NoError(t, err)
		seasonal := items[1].Hash
		if sid == "tt1234567:2:2" {
			seasonal = items[2].Hash
		}
		assert.ElementsMatch(t, []string{items[0].Hash, seasonal}, hashes)
		// Peer export must agree with local candidate lookup.
		data, err := ListByStremId(sid, false)
		require.NoError(t, err)
		exported := []string{}
		for _, item := range data.Items {
			exported = append(exported, item.Hash)
		}
		assert.ElementsMatch(t, hashes, exported)
	}
}

func TestPeerImportWithLaggingReplica(t *testing.T) {
	setupPeerImportTestDB(t)
	replica, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	replica.SetMaxOpenConns(1)
	require.NoError(t, goose.Up(replica, "../../migrations/sqlite"))
	t.Cleanup(func() { replica.Close() })
	db.SetReplicasForTesting([]*sql.DB{replica}, t.Cleanup)
	hash := peerTestHash()
	sid := "tt1234567:1:2"
	// Prime the file cache with an obsolete replica mapping.
	_, err = replica.Exec("INSERT INTO torrent_stream (h, p, sid, src) VALUES (?, ?, ?, ?)", hash, "/video.mkv", "tt7654321:1:2", "tio")
	require.NoError(t, err)
	staleFiles, err := ts.GetFilesByHashes([]string{hash})
	require.NoError(t, err)
	require.Equal(t, "tt7654321:1:2", staleFiles[hash][0].SId)
	require.NoError(t, UpsertFromPeer([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio,
		Files: ts.Files{{Path: "/video.mkv", Name: "video.mkv", SId: sid}},
	}}, "tt1234567"))
	// A replica-backed read still sees nothing; all import-dependent reads must
	// obtain the primary's data, including bypassing stale cached files.
	stale, err := GetByHashes([]string{hash})
	require.NoError(t, err)
	assert.Empty(t, stale)
	primary, err := GetByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.Contains(t, primary, hash)
	assert.True(t, primary[hash].IsParsed())
	// Re-prime after import invalidation to verify primary reads bypass cache.
	_, err = ts.GetFilesByHashes([]string{hash})
	require.NoError(t, err)
	files, err := ts.GetFilesByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.Equal(t, sid, files[hash][0].SId)
	// Repeated primary reads must remain independent of stale replica cache.
	repeated, err := ts.GetFilesByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.Equal(t, files, repeated)
	// Primary reads must immediately include subsequent peer writes.
	require.NoError(t, ts.RecordFromPeer([]ts.InsertData{{Hash: hash, File: ts.File{Path: "/second.mkv", SId: sid, Source: "tio"}}}, false))
	files, err = ts.GetFilesByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.Len(t, files[hash], 2)
	hashes, err := ListHashesByStremIdFromPrimary(sid)
	require.NoError(t, err)
	assert.Equal(t, []string{hash}, hashes)
}
