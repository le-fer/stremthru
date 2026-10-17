package torrent_info

import (
	"database/sql"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// Run this test with STREMTHRU_DATABASE_URI pointing to a disposable PostgreSQL
// database to exercise real MVCC snapshots and transaction advisory locks.
// Each run owns an isolated database; the normal suite uses SQLite.
func setupPeerMappingTestDB(t *testing.T) *sql.DB {
	t.Helper()
	if db.Dialect != db.DBDialectPostgres {
		conn, err := sql.Open("sqlite3", ":memory:")
		require.NoError(t, err)
		conn.SetMaxOpenConns(1)
		goose.SetLogger(goose.NopLogger())
		require.NoError(t, goose.SetDialect("sqlite"))
		require.NoError(t, goose.Up(conn, "../../migrations/sqlite"))
		db.SetForTesting(conn, t.Cleanup)
		return conn
	}
	admin, err := sql.Open("pgx", config.DatabaseURI)
	require.NoError(t, err)
	database := "peer_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec("CREATE DATABASE " + database)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec("DROP DATABASE " + database + " WITH (FORCE)")
		require.NoError(t, err)
		admin.Close()
	})
	uri, err := url.Parse(config.DatabaseURI)
	require.NoError(t, err)
	query := uri.Query()
	uri.Path = "/" + database
	query.Set("statement_timeout", "10000")
	uri.RawQuery = query.Encode()
	conn, err := sql.Open("pgx", uri.String())
	require.NoError(t, err)
	conn.SetMaxOpenConns(4)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(conn, "../../migrations/postgres"))
	db.SetForTesting(conn, t.Cleanup)
	return conn
}

func TestPeerConcurrentFallbackMappings(t *testing.T) {
	setupPeerMappingTestDB(t)
	seedPeerIMDbTitle(t, "tt1234567", "Diagnostic Programme", "", 0)
	seedPeerIMDbTitle(t, "tt7654321", "Diagnostic Programme", "", 0)
	hash := peerTestHash()
	items := []TorrentInfoInsertData{{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio}}
	require.NoError(t, Upsert(items, "", false))
	_, parseErr := parsePeerImports([]string{hash})
	require.NoError(t, parseErr)
	if db.Dialect == db.DBDialectPostgres {
		// Hold both competing inserts after their eligibility snapshots. Without
		// the shared lock, distinct tids satisfy the composite conflict target.
		_, err := db.Exec(`CREATE FUNCTION delay_peer_mapping() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN PERFORM pg_sleep(0.2); RETURN NEW; END $$`)
		require.NoError(t, err)
		_, err = db.Exec(`CREATE TRIGGER delay_peer_mapping BEFORE INSERT ON imdb_torrent
			FOR EACH ROW EXECUTE FUNCTION delay_peer_mapping()`)
		require.NoError(t, err)
	}
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, sid := range []string{"tt1234567", "tt7654321"} {
		go func() {
			<-start
			done <- UpsertFromPeer(items, sid)
		}()
	}
	close(start)
	// Join both importers before restoring shared test database state.
	first, second := <-done, <-done
	require.NoError(t, first)
	require.NoError(t, second)
	var count int
	require.NoError(t, db.GetPrimary().QueryRow("SELECT COUNT(*) FROM imdb_torrent WHERE hash = ?", hash).Scan(&count))
	require.Equal(t, 1, count, "concurrent fallback imports must not create competing title mappings")
}

func TestPeerConcurrentParses(t *testing.T) {
	if db.Dialect != db.DBDialectPostgres {
		t.Skip("requires PostgreSQL row locks")
	}
	setupPeerMappingTestDB(t)
	hashes := []string{}
	items := []TorrentInfoInsertData{}
	for range 12 {
		hash := peerTestHash()
		hashes = append(hashes, hash)
		items = append(items, TorrentInfoInsertData{Hash: hash, TorrentTitle: "Diagnostic.Programme.S01E02", Source: TorrentInfoSourceTorrentio})
	}
	require.NoError(t, Upsert(items, "", false))
	// Let concurrent parsers overlap while each owns some row locks. Parsed
	// snapshots originate from maps, whose iteration order differs per request.
	_, err := db.Exec(`CREATE FUNCTION slow_peer_parse() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.parser_input != '' THEN PERFORM pg_sleep(0.02); END IF; RETURN NEW; END $$`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER slow_peer_parse BEFORE INSERT ON torrent_info
		FOR EACH ROW EXECUTE FUNCTION slow_peer_parse()`)
	require.NoError(t, err)
	start := make(chan struct{})
	done := make(chan error, 4)
	for range 4 {
		go func() {
			<-start
			_, err := parsePeerImports(hashes)
			done <- err
		}()
	}
	close(start)
	errs := make([]error, 4)
	for i := range errs {
		errs[i] = <-done
	}
	for _, err := range errs {
		require.NoError(t, err, "concurrent synchronous peer parsers must complete")
	}
	stored, err := GetByHashesFromPrimary(hashes)
	require.NoError(t, err)
	for _, info := range stored {
		require.True(t, info.IsParsed())
	}
}

func TestMetadataWritePreservesInputAndDuplicatePrecedence(t *testing.T) {
	setupPeerMappingTestDB(t)
	hashes := []string{peerTestHash(), peerTestHash()}
	slices.Sort(hashes)
	items := []TorrentInfoInsertData{
		{Hash: hashes[1], TorrentTitle: "First.Series.S01E02", Source: TorrentInfoSourceTorrentio},
		{Hash: hashes[0], TorrentTitle: "Other.Series.S01E02", Source: TorrentInfoSourceTorrentio},
		{Hash: hashes[1], TorrentTitle: "Duplicate.Series.S02E03", Source: TorrentInfoSourceTorrentio},
	}
	original := slices.Clone(items)
	require.NoError(t, Upsert(items, "", false))
	require.Equal(t, original, items, "metadata ordering must not mutate caller input")
	stored, err := GetByHashesFromPrimary(hashes)
	require.NoError(t, err)
	require.Equal(t, items[0].TorrentTitle, stored[hashes[1]].TorrentTitle, "first duplicate retains precedence")
	parsed := make([]*TorrentInfo, 0, len(hashes))
	for _, hash := range []string{hashes[1], hashes[0]} {
		info := stored[hash]
		require.NoError(t, info.Parse())
		parsed = append(parsed, &info)
	}
	originalParsed := slices.Clone(parsed)
	require.NoError(t, UpsertParsed(parsed))
	require.Equal(t, originalParsed, parsed, "parsed-write ordering must not mutate caller input")
}

func TestMetadataWritePreservesCrossChunkDuplicates(t *testing.T) {
	setupPeerMappingTestDB(t)
	hash := peerTestHash()
	items := []TorrentInfoInsertData{{Hash: hash, TorrentTitle: "First.Series.S01E02", Source: TorrentInfoSourceTorrentio}}
	for range 149 {
		items = append(items, TorrentInfoInsertData{Hash: peerTestHash(), TorrentTitle: "Other.Series.S01E02", Source: TorrentInfoSourceTorrentio})
	}
	items = append(items, TorrentInfoInsertData{Hash: hash, TorrentTitle: "Authoritative.Series.S02E03", Source: TorrentInfoSourceDHT})
	original := slices.Clone(items)
	require.NoError(t, Upsert(items, "", false))
	require.Equal(t, original, items)
	stored, err := GetByHashesFromPrimary([]string{hash})
	require.NoError(t, err)
	require.Equal(t, items[150].TorrentTitle, stored[hash].TorrentTitle, "a stronger source in a later transaction must retain precedence")
	require.Equal(t, string(TorrentInfoSourceDHT), stored[hash].Source)

	// Parsed writes have their own 200-row transaction boundary. Keep a later
	// occurrence in its original transaction so PostgreSQL never sees the same
	// conflict target twice in one INSERT.
	parsed := make([]*TorrentInfo, 0, 201)
	for range 200 {
		info := &TorrentInfo{
			Hash: peerTestHash(), TorrentTitle: "Diagnostic.Programme.S01E02", Source: string(TorrentInfoSourceTorrentio),
			CreatedAt: db.Timestamp{Time: time.Now()}, UpdatedAt: db.Timestamp{Time: time.Now()},
		}
		require.NoError(t, info.Parse())
		parsed = append(parsed, info)
	}
	parsed = append(parsed, parsed[0])
	originalParsed := slices.Clone(parsed)
	require.NoError(t, UpsertParsed(parsed))
	require.Equal(t, originalParsed, parsed)
}
