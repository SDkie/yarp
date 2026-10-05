// Package cache is a key/value store backed by BadgerDB.
package cache

import (
	"errors"
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// DefaultDir is where yarp stores the HTTP cache, relative to the working
// directory.
const DefaultDir = ".cache"

// Cache is a key/value store backed by BadgerDB. It is safe for concurrent
// use.
type Cache struct {
	db *badger.DB
}

// Open opens the store in dir, creating it if needed. Only one Cache may use
// a dir at a time. The caller must call Close when done.
func Open(dir string) (*Cache, error) {
	opts := badger.DefaultOptions(dir).WithLogger(badgerLogger{})
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open cache %q: %w", dir, err)
	}
	return &Cache{db: db}, nil
}

// Get returns the value stored under key. found is false when the key does
// not exist or has expired; err is only set for real failures.
func (c *Cache) Get(key string) (value []byte, found bool, err error) {
	err = c.db.View(func(txn *badger.Txn) error {
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
func (c *Cache) Set(key string, value []byte, ttl time.Duration) error {
	err := c.db.Update(func(txn *badger.Txn) error {
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
func (c *Cache) Delete(key string) error {
	err := c.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
	if err != nil {
		return fmt.Errorf("cache delete %q: %w", key, err)
	}
	return nil
}

// Close flushes pending writes and releases the store.
func (c *Cache) Close() error {
	return c.db.Close()
}
