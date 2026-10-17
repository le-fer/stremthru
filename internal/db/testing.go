package db

import "database/sql"

// SetForTesting redirects db queries to sqlDB, undone via cleanup (pass t.Cleanup).
func SetForTesting(sqlDB *sql.DB, cleanup func(func())) {
	prev := db.DB
	prevReplicas := db.replicas
	db.DB = sqlDB
	db.replicas = nil
	cleanup(func() {
		db.DB = prev
		db.replicas = prevReplicas
		sqlDB.Close()
	})
}

// SetReplicasForTesting simulates replica reads independently of the primary.
// The caller owns the connections and must close them after cleanup restores
// the previous replicas.
func SetReplicasForTesting(replicas []*sql.DB, cleanup func(func())) {
	prev := db.replicas
	db.replicas = replicas
	cleanup(func() { db.replicas = prev })
}
