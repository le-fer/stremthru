package torrent_info

import (
	"fmt"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_torrent"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMappingLockBudget(t *testing.T) {
	if db.Dialect != db.DBDialectPostgres {
		t.Skip("PostgreSQL advisory-lock capacity regression")
	}
	setupPeerMappingTestDB(t)
	hashes := make([]string, 20_000)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("lock-budget-%d", i)
	}
	var count int
	err := imdb_torrent.WithHashLocks(hashes, func(executor db.Executor) error {
		return executor.QueryRow("SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()").Scan(&count)
	})
	require.NoError(t, err, "large batches must not exhaust PostgreSQL shared lock memory")
	require.Positive(t, count)
	require.LessOrEqual(t, count, 256, "lock cardinality must stay bounded independently of batch size")
}

func TestIMDbHashIndexMigrationRoundTrip(t *testing.T) {
	conn := setupPeerMappingTestDB(t)
	hash := peerTestHash()
	_, err := db.Exec("INSERT INTO torrent_stream (h,p,sid,src) VALUES (?,?,?,?)", hash, "/video.mkv", "tt1234567:1:1", "tio")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO imdb_torrent (hash,tid) VALUES (?,?)", hash, "tt1234567")
	require.NoError(t, err)
	// Rolling back this index must preserve mapping/file data and permit writes.
	migrations := "../../migrations/" + string(db.Dialect)
	if db.Dialect == db.DBDialectPostgres {
		migrations = "../../migrations/postgres"
	}
	require.NoError(t, goose.DownTo(conn, migrations, 20260526120000))
	var indexes int
	if db.Dialect == db.DBDialectPostgres {
		require.NoError(t, conn.QueryRow("SELECT COUNT(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'imdb_torrent_idx_hash'").Scan(&indexes))
	} else {
		require.NoError(t, conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'imdb_torrent_idx_hash'").Scan(&indexes))
	}
	require.Zero(t, indexes, "rollback must remove the added index")
	_, err = db.Exec("UPDATE torrent_stream SET sid = ? WHERE h = ?", "tt1234567:1:2", hash)
	require.NoError(t, err)
	var sid string
	require.NoError(t, db.GetPrimary().QueryRow("SELECT sid FROM torrent_stream WHERE h = ?", hash).Scan(&sid))
	require.Equal(t, "tt1234567:1:2", sid)
	require.NoError(t, goose.Up(conn, migrations))
	require.NoError(t, db.GetPrimary().QueryRow("SELECT sid FROM torrent_stream WHERE h = ?", hash).Scan(&sid))
	require.Equal(t, "tt1234567:1:2", sid)
	var tid string
	require.NoError(t, db.GetPrimary().QueryRow("SELECT tid FROM imdb_torrent WHERE hash = ?", hash).Scan(&tid))
	require.Equal(t, "tt1234567", tid)
	// The hash-leading index must exist after upgrade on both supported engines.
	if db.Dialect == db.DBDialectPostgres {
		var index string
		require.NoError(t, conn.QueryRow("SELECT indexdef FROM pg_indexes WHERE tablename = 'imdb_torrent' AND indexname = 'imdb_torrent_idx_hash'").Scan(&index))
		require.Contains(t, index, "(hash)")
	} else {
		var column string
		require.NoError(t, conn.QueryRow("SELECT name FROM pragma_index_info('imdb_torrent_idx_hash') WHERE seqno = 0").Scan(&column))
		require.Equal(t, "hash", column)
	}
}
