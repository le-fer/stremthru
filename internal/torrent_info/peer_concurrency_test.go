package torrent_info

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestPeerImportPreservesCompetingParse(t *testing.T) {
	setupPeerImportTestDB(t)
	hash := peerTestHash()
	// Simulate a competing metadata/parser write between the import's read and
	// its parsed UPSERT, at the exact SQL write boundary.
	_, err := db.Exec(`CREATE TRIGGER competing_parser BEFORE INSERT ON torrent_info
 WHEN NEW.parser_input != '' BEGIN
 UPDATE torrent_info SET t_title = 'Diagnostic.Programme.S02E02', parser_input = 'Diagnostic.Programme.S02E02', title = 'Diagnostic Programme', seasons = '2', episodes = '2' WHERE hash = NEW.hash;
 END`)
	require.NoError(t, err)
	require.NoError(t, UpsertFromPeer([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}, "tt1234567"))
	stored, err := GetByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	info := stored[hash]
	t.Logf("stored title=%q parser input=%q seasons=%v episodes=%v", info.TorrentTitle, info.ParserInput, info.Seasons, info.Episodes)
	require.True(t, info.IsParsed(), "successful synchronous import must not overwrite a competing parse with stale results")
	hashes, err := ListHashesByStremIdFromPrimary("tt1234567:2:2")
	require.NoError(t, err)
	require.Contains(t, hashes, hash)
}

type rowErrorExecutor struct{ db.Executor }

func (e rowErrorExecutor) Query(query string, args ...any) (*sql.Rows, error) {
	// Failing while stepping rows emulates a streaming database read failure.
	query = strings.Replace(query, `"hash"`, `metadata_row_error("hash")`, 1)
	return e.Executor.Query(query, args...)
}
func TestGetByHashesPropagatesRowsError(t *testing.T) {
	driverName := "metadata_row_error_" + peerTestHash()
	sql.Register(driverName, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("metadata_row_error", func(hash string) (string, error) { return "", errors.New("injected streaming metadata failure") }, true)
	}})
	conn, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(conn, "../../migrations/sqlite"))
	db.SetForTesting(conn, t.Cleanup)
	hash := peerTestHash()
	require.NoError(t, Upsert([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}, "", false))
	info, err := getByHashes([]string{hash}, rowErrorExecutor{db.GetPrimary()})
	t.Logf("returned rows=%d err=%v", len(info), err)
	require.Error(t, err, "streaming metadata read failure must propagate to synchronous import")
}

func TestPeerImportRetriesChangedTitle(t *testing.T) {
	setupPeerImportTestDB(t)
	hash := peerTestHash()
	_, err := db.Exec(`CREATE TRIGGER competing_title BEFORE INSERT ON torrent_info
 WHEN NEW.parser_input = 'Diagnostic.Programme.S01E02' BEGIN
 UPDATE torrent_info SET t_title = 'Diagnostic.Programme.S02E02' WHERE hash = NEW.hash;
 END`)
	require.NoError(t, err)
	require.NoError(t, UpsertFromPeer([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}, "tt1234567"))
	stored, err := GetByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.True(t, stored[hash].IsParsed())
	require.Equal(t, "Diagnostic.Programme.S02E02", stored[hash].ParserInput)
	hashes, err := ListHashesByStremIdFromPrimary("tt1234567:2:2")
	require.NoError(t, err)
	require.Equal(t, []string{hash}, hashes)
	hashes, err = ListHashesByStremIdFromPrimary("tt1234567:1:2")
	require.NoError(t, err)
	require.Empty(t, hashes)
}

func TestPeerImportBoundsRetriesForChangingTitle(t *testing.T) {
	setupPeerImportTestDB(t)
	hash := peerTestHash()
	_, err := db.Exec(`CREATE TRIGGER changing_title BEFORE INSERT ON torrent_info
 WHEN NEW.parser_input != '' BEGIN
 UPDATE torrent_info SET t_title = CASE t_title WHEN 'Diagnostic.Programme.S01E02' THEN 'Diagnostic.Programme.S02E02' ELSE 'Diagnostic.Programme.S01E02' END WHERE hash = NEW.hash;
 END`)
	require.NoError(t, err)
	err = UpsertFromPeer([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}, "tt1234567")
	require.ErrorContains(t, err, "torrent metadata changed while parsing peer import")
	var mappings int
	require.NoError(t, db.GetPrimary().QueryRow("SELECT COUNT(*) FROM imdb_torrent WHERE hash = ?", hash).Scan(&mappings))
	require.Zero(t, mappings, "an incomplete parse must not finalize the imported relationship")
	_, err = db.Exec("DROP TRIGGER changing_title")
	require.NoError(t, err)
	require.NoError(t, UpsertFromPeer([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}, "tt1234567"))
	stored, err := GetByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.True(t, stored[hash].IsParsed())
}

func TestUpsertParsedPreservesChangedTitle(t *testing.T) {
	setupPeerImportTestDB(t)
	hash := peerTestHash()
	require.NoError(t, Upsert([]TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}, "", false))
	snapshot, err := GetByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	stale := snapshot[hash]
	require.NoError(t, stale.Parse())
	// A different parser finishes the newer title before this snapshot is saved.
	_, err = db.Exec("UPDATE torrent_info SET t_title = ?, parser_input = ?, seasons = ?, episodes = ? WHERE hash = ?", "Diagnostic.Programme.S02E02", "Diagnostic.Programme.S02E02", "2", "2", hash)
	require.NoError(t, err)
	require.NoError(t, UpsertParsed([]*TorrentInfo{&stale}))
	current, err := GetByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.True(t, current[hash].IsParsed())
	require.Equal(t, CommaSeperatedInt{2}, current[hash].Seasons)
}
