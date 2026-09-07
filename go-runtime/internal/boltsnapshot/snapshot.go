// Package boltsnapshot implements the bbolt documented hot-backup boundary.
package boltsnapshot

import (
	"bytes"
	"errors"

	bolt "go.etcd.io/bbolt"
)

const MaximumBytes int64 = 32 << 20

// Read takes a consistent snapshot while the live database remains open.
// This preserves retired MDD operations.py's SQLite backup API semantics;
// copying the mutable database path directly is not a database backup.
func Read(db *bolt.DB) ([]byte, error) {
	if db == nil {
		return nil, errors.New("backup database is unavailable")
	}
	var output bytes.Buffer
	err := db.View(func(tx *bolt.Tx) error {
		if tx.Size() > MaximumBytes {
			return errors.New("database snapshot exceeds backup size limit")
		}
		_, err := tx.WriteTo(&output)
		return err
	})
	if err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
