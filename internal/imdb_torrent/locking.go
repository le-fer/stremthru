package imdb_torrent

import (
	"hash/fnv"
	"slices"

	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/util"
)

// Bound the global PostgreSQL lock namespace even when multiple instances map
// large batches concurrently. Collisions serialize writes; they do not affect
// mapping eligibility. Every participant acquires these stripes in sorted order.
const mappingLockStripes = 256

// WithHashLocks serializes mapping eligibility checks and mapping/file-ID writes
// for the same hashes. The callback must use the supplied executor.
// PostgreSQL locks use that transaction's connection, never another pool slot.
func WithHashLocks(hashes []string, write func(db.Executor) error) error {
	if len(hashes) == 0 {
		return write(db.GetPrimary())
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if db.Dialect == db.DBDialectPostgres {
		keys := make([]int32, 0, len(hashes))
		for _, hash := range hashes {
			h := fnv.New32a()
			h.Write([]byte(hash))
			keys = append(keys, int32(h.Sum32()%mappingLockStripes))
		}
		slices.Sort(keys)
		keys = slices.Compact(keys)
		args := make([]any, 1, len(keys)+1)
		// Stable namespace for torrent mapping locks.
		args[0] = int32(0x746d6170)
		for _, key := range keys {
			args = append(args, key)
		}
		// Sort the actual lock keys before acquisition, including hash collisions.
		query := `SELECT pg_advisory_xact_lock(?, k) FROM
			(SELECT k FROM (VALUES ` + util.RepeatJoin("(CAST(? AS INTEGER))", len(keys), ",") + `) AS keys(k) ORDER BY k) AS ordered_keys`
		_, err = tx.Exec(query, args...)
	} else {
		// Obtain SQLite's writer lock before any predicate reads. A deferred read
		// transaction cannot safely upgrade after another writer commits.
		_, err = tx.Exec("UPDATE imdb_torrent SET hash = hash WHERE 0")
	}
	if err != nil {
		return err
	}
	if err := write(tx); err != nil {
		return err
	}
	return tx.Commit()
}
