package boltsnapshot

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestBackupSupportsRealDatabaseAboveOld32MiBLimit(t *testing.T) {
	root := t.TempDir()
	db, err := bolt.Open(filepath.Join(root, "large.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	value := bytes.Repeat([]byte{0x5a}, 1<<20)
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucket([]byte("large"))
		if err != nil {
			return err
		}
		for i := 0; i < 34; i++ {
			if err := bucket.Put([]byte(fmt.Sprintf("item-%02d", i)), value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data, err := Read(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) <= 32<<20 {
		t.Fatal("fixture does not exercise the former size limit")
	}
	path := filepath.Join(root, "snapshot.db")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.View(func(tx *bolt.Tx) error {
		if !bytes.Equal(tx.Bucket([]byte("large")).Get([]byte("item-33")), value) {
			t.Error("large snapshot lost value")
		}
		for err := range tx.Check() {
			if err != nil {
				t.Error(err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotReopensConsistentlyDuringWrites(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "live.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	write := func(n uint64) error {
		return db.Update(func(tx *bolt.Tx) error {
			bucket, err := tx.CreateBucketIfNotExists([]byte("pairs"))
			if err != nil {
				return err
			}
			var value [8]byte
			binary.BigEndian.PutUint64(value[:], n)
			if err := bucket.Put([]byte("left"), value[:]); err != nil {
				return err
			}
			return bucket.Put([]byte("right"), value[:])
		})
	}
	if err := write(0); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := uint64(1); i <= 100; i++ {
			if err := write(i); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	var snapshotErr error
	for i := 0; i < 5; i++ {
		data, err := Read(db)
		if err != nil {
			snapshotErr = err
			break
		}
		path := filepath.Join(t.TempDir(), "snapshot.db")
		if err = os.WriteFile(path, data, 0600); err != nil {
			snapshotErr = err
			break
		}
		copyDB, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
		if err != nil {
			snapshotErr = err
			break
		}
		snapshotErr = copyDB.View(func(tx *bolt.Tx) error {
			var checkErr error
			for err := range tx.Check() {
				if err != nil && checkErr == nil {
					checkErr = err
				}
			}
			if checkErr != nil {
				return checkErr
			}
			bucket := tx.Bucket([]byte("pairs"))
			if binary.BigEndian.Uint64(bucket.Get([]byte("left"))) != binary.BigEndian.Uint64(bucket.Get([]byte("right"))) {
				t.Error("torn transactional snapshot")
			}
			return nil
		})
		if err := copyDB.Close(); snapshotErr == nil {
			snapshotErr = err
		}
		if snapshotErr != nil {
			break
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
}

func TestClosedDatabaseCannotProduceBackup(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "closed.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := Read(db); err == nil || data != nil {
		t.Fatal("closed database produced backup")
	}
}
