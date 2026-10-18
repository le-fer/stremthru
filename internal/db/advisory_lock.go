package db

import (
	"context"
	"database/sql"
	"errors"
	"hash/crc32"
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

// Bound dedicated lock connections per process independently of the application
// pool. PostgreSQL must have capacity for both pools across all instances.
const (
	postgresAdvisoryLockMaxConns                = 8
	postgresAdvisoryLockAcquireTimeout          = 30 * time.Second
	postgresAdvisoryLockMigrationAcquireTimeout = 10 * time.Minute
	postgresAdvisoryLockCleanupTimeout          = 5 * time.Second
)

var postgresAdvisoryLockSlots = make(chan struct{}, postgresAdvisoryLockMaxConns)

type postgresAdvisoryLock struct {
	Executor
	pool           *sql.DB
	conn           *sql.Conn
	tx             *sql.Tx
	name           string
	count          int
	err            error
	keyA           int32
	keyB           int32
	hasSlot        bool
	txCancel       context.CancelFunc
	acquireTimeout time.Duration
}

// database/sql has no context-taking Commit or Rollback. The pgx adapter uses
// the original BeginTx context for both, so cancel that context if cleanup stalls.
func (l *postgresAdvisoryLock) limitTransactionCleanup() func() {
	if l.txCancel == nil {
		return func() {}
	}
	timer := time.AfterFunc(postgresAdvisoryLockCleanupTimeout, l.txCancel)
	return func() { timer.Stop() }
}

func (l *postgresAdvisoryLock) cleanup() {
	l.Executor = nil
	l.count = 0

	if l.tx != nil {
		stop := l.limitTransactionCleanup()
		if err := l.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			l.err = errors.Join(l.err, err)
			lockLog.Error("lock tx rollback failed", "error", err, "name", l.name)
		}
		stop()
		l.tx = nil
	}
	if l.txCancel != nil {
		l.txCancel()
		l.txCancel = nil
	}

	if l.conn != nil {
		if err := l.conn.Close(); err != nil {
			l.err = errors.Join(l.err, err)
			lockLog.Error("lock connection close failed", "error", err, "name", l.name)
		}
		l.conn = nil
	}

	l.pool = nil

	if l.hasSlot {
		<-postgresAdvisoryLockSlots
		l.hasSlot = false
	}
}

func (l *postgresAdvisoryLock) commit() bool {
	if l.tx == nil {
		l.cleanup()
		return false
	}

	stop := l.limitTransactionCleanup()
	commitErr := l.tx.Commit()
	stop()
	l.tx = nil
	if commitErr != nil {
		l.err = errors.Join(l.err, commitErr)
		lockLog.Error("lock tx commit failed", "error", commitErr, "name", l.name)
	}

	l.cleanup()
	return commitErr == nil && l.err == nil
}

func (l *postgresAdvisoryLock) GetName() string {
	return l.name
}

// open reserves capacity and connects only when acquisition is attempted.
func (l *postgresAdvisoryLock) open(ctx context.Context, try bool) (stage string, opened bool, err error) {
	if l.pool == nil {
		return "open", false, errors.New("lock connection closed")
	}
	if l.tx != nil {
		return "", true, nil
	}
	if try {
		select {
		case postgresAdvisoryLockSlots <- struct{}{}:
		default:
			return "capacity", false, nil
		}
	} else {
		select {
		case postgresAdvisoryLockSlots <- struct{}{}:
		case <-ctx.Done():
			return "capacity", false, ctx.Err()
		}
	}
	l.hasSlot = true

	conn, err := l.pool.Conn(ctx)
	if err != nil {
		return "connect", false, err
	}
	l.conn = conn

	// Bound BEGIN without tying the lifetime of a held lock to the acquisition
	// deadline. Stop forwarding cancellation once transaction setup completes.
	txCtx, cancel := context.WithCancel(context.Background())
	l.txCancel = cancel
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	tx, err := conn.BeginTx(txCtx, nil)
	if err != nil {
		return "begin", false, err
	}
	l.tx = tx
	l.Executor = &Tx{tx: tx, exec: getExec(tx)}
	return "begin", true, ctx.Err()
}

func (l *postgresAdvisoryLock) acquire(ctx context.Context, try bool) (acquired bool) {
	started := time.Now()
	defer func() {
		if !acquired {
			l.cleanup()
		}
	}()

	stage, opened, err := l.open(ctx, try)
	if err == nil && !opened {
		lockLog.Debug("try acquire skipped, lock connection capacity full", "name", l.name, "limit", postgresAdvisoryLockMaxConns)
		return false
	}
	if err == nil {
		stage = "lock"
		if try {
			err = l.tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1, $2)", l.keyA, l.keyB).Scan(&acquired)
		} else {
			_, err = l.tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1, $2)", l.keyA, l.keyB)
			acquired = err == nil
		}
	}
	if err != nil {
		l.err = errors.Join(l.err, err)
		lockLog.Error("acquire failed", "error", err, "name", l.name, "stage", stage, "try", try, "elapsed", time.Since(started))
		return false
	}
	if acquired {
		l.count++
	} else {
		lockLog.Debug("try acquire skipped, lock held by another session", "name", l.name, "count", l.count)
	}
	return acquired
}

func (l *postgresAdvisoryLock) Acquire() bool {
	ctx, cancel := context.WithTimeout(context.Background(), l.acquireTimeout)
	defer cancel()
	return l.acquire(ctx, false)
}

func (l *postgresAdvisoryLock) TryAcquire() bool {
	ctx, cancel := context.WithTimeout(context.Background(), postgresAdvisoryLockAcquireTimeout)
	defer cancel()
	return l.acquire(ctx, true)
}

func (l *postgresAdvisoryLock) Release() bool {
	if l.count == 0 || l.Executor == nil {
		l.cleanup()
		return false
	}

	l.count--
	if l.count == 0 {
		return l.commit()
	}

	return true
}

func (l *postgresAdvisoryLock) ReleaseAll() bool {
	if l.count == 0 {
		l.cleanup()
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

	if db.advisoryLocks == nil {
		lockLog.Error("lock database pool unavailable", "name", name)
		return nil
	}

	keyA, keyB := getAdvisoryLockKeyPair(names...)
	acquireTimeout := postgresAdvisoryLockAcquireTimeout
	// Concurrent startup must allow time for another instance's migrations.
	if len(names) == 2 && names[0] == "goose" && names[1] == "migration" {
		acquireTimeout = postgresAdvisoryLockMigrationAcquireTimeout
	}
	return &postgresAdvisoryLock{
		pool:           db.advisoryLocks,
		name:           name,
		keyA:           int32(keyA),
		keyB:           int32(keyB),
		acquireTimeout: acquireTimeout,
	}
}
