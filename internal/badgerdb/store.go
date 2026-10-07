// Package badgerdb is a key/value store backed by BadgerDB.
package badgerdb

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// DefaultDir is the default directory of the store, relative to the working
// directory.
const DefaultDir = ".cache"

// Store is a key/value store backed by BadgerDB. It is safe for concurrent
// use.
type Store struct {
	db *badger.DB
}

// Open opens the store in dir, creating it if needed. Only one Store may use
// a dir at a time. The caller must call Close when done.
func Open(dir string) (*Store, error) {
	opts := badger.DefaultOptions(dir).WithLogger(badgerLogger{})
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open cache %q: %w", dir, err)
	}
	return &Store{db: db}, nil
}

// Get returns the value stored under key. found is false when the key does
// not exist or has expired; err is only set for real failures.
func (s *Store) Get(key string) (value []byte, found bool, err error) {
	err = s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		value, err = item.ValueCopy(nil)
		return err
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cache get %q: %w", key, err)
	}
	return value, true, nil
}

// Set stores value under key. With ttl > 0 the entry expires after ttl;
// with ttl == 0 it never expires.
func (s *Store) Set(key string, value []byte, ttl time.Duration) error {
	err := s.db.Update(func(txn *badger.Txn) error {
		entry := badger.NewEntry([]byte(key), value)
		if ttl > 0 {
			entry = entry.WithTTL(ttl)
		}
		return txn.SetEntry(entry)
	})
	if err != nil {
		return fmt.Errorf("cache set %q: %w", key, err)
	}
	return nil
}

// Delete removes key. Deleting a key that does not exist is not an error.
func (s *Store) Delete(key string) error {
	err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
	if err != nil {
		return fmt.Errorf("cache delete %q: %w", key, err)
	}
	return nil
}

// Close flushes pending writes and releases the store.
func (s *Store) Close() error {
	err := s.db.Close()
	if err != nil {
		slog.Error("failed to close cache", "error", err)
	}

	return err
}
