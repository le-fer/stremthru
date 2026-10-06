package db

import (
	"context"
	"database/sql"
	"errors"
	"hash/crc32"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/MunifTanjim/stremthru/internal/logger"
)

var lockLog = logger.Scoped("db/advisory_lock")

var stremthruChecksum = crc32.ChecksumIEEE([]byte("STREMTHRU"))

func getAdvisoryLockKeyPair(names ...string) (uint32, uint32) {
	return stremthruChecksum, crc32.ChecksumIEEE([]byte(strings.Join(names, string(rune(0)))))
}

type AdvisoryLock interface {
	Executor
	GetName() string
	Acquire() bool
	TryAcquire() bool
	Release() bool
	ReleaseAll() bool
	Err() error
}

type sqliteAdvisoryLock struct {
	Executor
	name   string
	locked bool
	m      sync.Mutex
}

var sqliteAdvisoryLockByName sync.Map

func (l *sqliteAdvisoryLock) lock() bool {
	l.m.Lock()
	defer l.m.Unlock()
	if !l.locked {
		l.locked = true
	}
	return l.locked
}

func (l *sqliteAdvisoryLock) unlock() bool {
	l.m.Lock()
	defer l.m.Unlock()
	if l.locked {
		l.locked = false
	}
	return !l.locked
}

func (l *sqliteAdvisoryLock) GetName() string {
	return l.name
}

func (l *sqliteAdvisoryLock) Acquire() bool {
	tryLeft := 5
	for tryLeft > 0 && !l.lock() {
		tryLeft--
		time.Sleep(1 * time.Second)
	}
	return l.locked
}

func (l *sqliteAdvisoryLock) TryAcquire() bool {
	return l.lock()
}

func (l *sqliteAdvisoryLock) Release() bool {
	return l.unlock()
}

func (l *sqliteAdvisoryLock) ReleaseAll() bool {
	return l.Release()
}

func (l *sqliteAdvisoryLock) Err() error {
	return nil
}

func sqliteNewAdvisoryLock(names ...string) AdvisoryLock {
	name := strings.Join(names, ":")
	if lock, ok := sqliteAdvisoryLockByName.Load(name); ok {
		return lock.(*sqliteAdvisoryLock)
	}
	lock := &sqliteAdvisoryLock{Executor: db, name: name}
	sqliteAdvisoryLockByName.Store(name, lock)
	return lock
}

type postgresAdvisoryLockExecutor struct {
	conn *sql.Conn
}

func (e *postgresAdvisoryLockExecutor) Exec(query string, args ...any) (sql.Result, error) {
	return e.conn.ExecContext(context.Background(), adaptQuery(query), args...)
}

func (e *postgresAdvisoryLockExecutor) Query(query string, args ...any) (*sql.Rows, error) {
	return e.conn.QueryContext(context.Background(), adaptQuery(query), args...)
}

func (e *postgresAdvisoryLockExecutor) QueryRow(query string, args ...any) *sql.Row {
	return e.conn.QueryRowContext(context.Background(), adaptQuery(query), args...)
}

type postgresAdvisoryLock struct {
	Executor
	lockDB *sql.DB
	conn   *sql.Conn
	name   string
	count  int
	err    error
	keyA   int32
	keyB   int32
}

func (l *postgresAdvisoryLock) close() {
	l.Executor = nil
	l.count = 0

	if l.conn != nil {
		if err := l.conn.Close(); err != nil {
			l.err = errors.Join(l.err, err)
			lockLog.Error("lock connection close failed", "error", err, "name", l.name)
		}
		l.conn = nil
	}

	if l.lockDB != nil {
		if err := l.lockDB.Close(); err != nil {
			l.err = errors.Join(l.err, err)
			lockLog.Error("lock database close failed", "error", err, "name", l.name)
		}
		l.lockDB = nil
	}
}

func (l *postgresAdvisoryLock) GetName() string {
	return l.name
}

func (l *postgresAdvisoryLock) Acquire() bool {
	if l.Executor == nil {
		lockLog.Error("acquire failed, lock connection closed", "name", l.name)
		return false
	}

	_, err := l.Exec("SELECT pg_advisory_lock(?, ?)", l.keyA, l.keyB)
	if err != nil {
		l.err = errors.Join(l.err, err)
		lockLog.Error("acquire failed", "error", err, "name", l.name)
		l.close()
		return false
	}

	l.count++
	return true
}

func (l *postgresAdvisoryLock) TryAcquire() bool {
	if l.Executor == nil {
		lockLog.Error("try acquire failed, lock connection closed", "name", l.name)
		return false
	}

	row := l.QueryRow("SELECT pg_try_advisory_lock(?, ?)", l.keyA, l.keyB)

	var acquired bool
	if err := row.Scan(&acquired); err != nil {
		l.err = errors.Join(l.err, err)
		lockLog.Error("try acquire failed", "error", err, "name", l.name)
		l.close()
		return false
	} else if !acquired {
		lockLog.Debug("try acquire failed", "name", l.name, "count", l.count)
		l.close()
		return false
	}

	l.count++
	return true
}

func (l *postgresAdvisoryLock) Release() bool {
	if l.count == 0 || l.Executor == nil {
		l.close()
		return false
	}

	row := l.QueryRow("SELECT pg_advisory_unlock(?, ?)", l.keyA, l.keyB)

	var released bool
	if err := row.Scan(&released); err != nil {
		l.err = errors.Join(l.err, err)
		lockLog.Error("release failed", "error", err, "name", l.name)
		l.close()
		return false
	} else if !released {
		lockLog.Debug("release failed", "name", l.name, "count", l.count)
		l.close()
		return false
	}

	l.count--
	if l.count == 0 {
		l.close()
	}

	return true
}

func (l *postgresAdvisoryLock) ReleaseAll() bool {
	if l.count == 0 {
		l.close()
		return false
	}

	for l.count > 0 {
		if !l.Release() {
			return false
		}
	}

	return true
}

func (l *postgresAdvisoryLock) Err() error {
	return l.err
}

func postgresNewAdvisoryLock(names ...string) AdvisoryLock {
	name := strings.Join(names, ":")

	// PostgreSQL advisory locks are session-scoped. Keep them on a dedicated
	// connection outside the application pool so code protected by the lock can
	// still use the normal pool without risking pool exhaustion or deadlock.
	lockDB, err := sql.Open(db.URI.DriverName, db.URI.DSN(func(_ *url.URL, q *url.Values) {
		q.Del("pool_max_conns")
		q.Del("pool_min_conns")
	}))
	if err != nil {
		lockLog.Error("lock database open failed", "error", err, "name", name)
		return nil
	}
	lockDB.SetMaxOpenConns(1)
	lockDB.SetMaxIdleConns(0)

	conn, err := lockDB.Conn(context.Background())
	if err != nil {
		_ = lockDB.Close()
		lockLog.Error("lock connection failed", "error", err, "name", name)
		return nil
	}

	keyA, keyB := getAdvisoryLockKeyPair(names...)
	return &postgresAdvisoryLock{
		Executor: &postgresAdvisoryLockExecutor{conn: conn},
		lockDB:   lockDB,
		conn:     conn,
		name:     name,
		keyA:     int32(keyA),
		keyB:     int32(keyB),
	}
}
