package testutil

import (
	"crypto/sha1"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

var fixtureSequence atomic.Uint64

// Hash gives each serial fixture a reproducible, distinct key, including repeats
// of a test in the same process, so unrelated process-wide caches cannot alias it.
func Hash(t *testing.T) string {
	return fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("%s:%d", t.Name(), fixtureSequence.Add(1)))))
}

func SetupDB(t *testing.T) {
	t.Helper()
	torrent_stream.ResetCachesForTesting(t.Cleanup)
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	db.SetForTesting(sqlDB, t.Cleanup)
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite"))
	require.NoError(t, goose.Up(sqlDB, filepath.Join(filepath.Dir(source), "../../migrations/sqlite")))
}
