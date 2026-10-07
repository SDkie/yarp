package badgerdb

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"golang.org/x/sync/errgroup"
)

// open opens a Store in dir and closes it when the test ends.
func open(t *testing.T, dir string) *Store {
	t.Helper()
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// get returns the value under key and whether it was found.
func get(t *testing.T, c *Store, key string) (string, bool) {
	t.Helper()
	v, found, err := c.Get(key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return string(v), found
}

func set(t *testing.T, c *Store, key, value string, ttl time.Duration) {
	t.Helper()
	if err := c.Set(key, []byte(value), ttl); err != nil {
		t.Fatalf("Set(%q): %v", key, err)
	}
}

// expiresAt returns the expiry Badger stored for key, in Unix seconds, or 0
// for none.
func expiresAt(t *testing.T, c *Store, key string) uint64 {
	t.Helper()
	var exp uint64
	err := c.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		exp = item.ExpiresAt()
		return nil
	})
	if err != nil {
		t.Fatalf("read expiry of %q: %v", key, err)
	}
	return exp
}

// TestSetGet checks that values read back as written, and that empty keys fail.
func TestSetGet(t *testing.T) {
	t.Parallel()
	c := open(t, t.TempDir())
	big := bytes.Repeat([]byte("x"), 1<<20+1) // past the store's 1 MB value threshold
	tests := []struct {
		name   string
		key    string
		value  []byte
		setErr bool
		getErr bool
	}{
		{"short", "short", []byte("v"), false, false},
		{"empty value", "empty", []byte{}, false, false},
		{"binary", "binary", []byte{0x00, 0xff, 0xfe, '\n', 0x00}, false, false},
		{"large", "large", big, false, false},
		{"empty key", "", []byte("v"), true, true}, // the store rejects empty keys
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set
			err := c.Set(tt.key, tt.value, 0)
			if (err != nil) != tt.setErr {
				t.Fatalf("Set(%q) error = %v, want error %v", tt.key, err, tt.setErr)
			}
			// Get
			v, found, err := c.Get(tt.key)
			if (err != nil) != tt.getErr {
				t.Fatalf("Get(%q) error = %v, want error %v", tt.key, err, tt.getErr)
			}
			if !tt.getErr && (!found || !bytes.Equal(v, tt.value)) {
				t.Errorf("Get(%q) = %d bytes, %v; want %d bytes, true", tt.key, len(v), found, len(tt.value))
			}
		})
	}
}

// TestSetOverwrite checks that a second Set replaces the first.
func TestSetOverwrite(t *testing.T) {
	t.Parallel()
	c := open(t, t.TempDir())

	set(t, c, "k", "v1", 0)
	set(t, c, "k", "v2", 0)
	if v, found := get(t, c, "k"); !found || v != "v2" {
		t.Errorf("Get after overwrite = %q, %v; want %q, true", v, found, "v2")
	}
}

// TestValuesAreCopied checks that the store shares no memory with the callers' slices.
func TestValuesAreCopied(t *testing.T) {
	t.Parallel()
	c := open(t, t.TempDir())
	for _, size := range []int{3, 1<<20 + 1} {
		key := fmt.Sprint("k", size)
		value := bytes.Repeat([]byte("a"), size)
		if err := c.Set(key, value, 0); err != nil {
			t.Fatal(err)
		}
		value[0] = 'X' // after Set returned

		got, _, err := c.Get(key)
		if err != nil || got[0] != 'a' {
			t.Fatalf("size %d: stored value changed with the caller's slice", size)
		}
		got[0] = 'Y'
		again, _, _ := c.Get(key)
		if again[0] != 'a' {
			t.Errorf("size %d: stored value changed with the returned slice", size)
		}
	}
}

// TestGetMissing checks that a missing key is not found and is not an error.
func TestGetMissing(t *testing.T) {
	t.Parallel()
	c := open(t, t.TempDir())
	set(t, c, "deleted", "v", 0)
	if err := c.Delete("deleted"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"never-set", "deleted"} {
		t.Run(key, func(t *testing.T) {
			v, found, err := c.Get(key)
			if v != nil || found || err != nil {
				t.Errorf("Get(%q) = %q, %v, %v; want nil, false, nil", key, v, found, err)
			}
		})
	}
}

// TestDelete checks deleting an existing, a missing and an empty key.
func TestDelete(t *testing.T) {
	t.Parallel()
	c := open(t, t.TempDir())

	// The key is gone afterwards.
	t.Run("existing key", func(t *testing.T) {
		set(t, c, "k", "v", 0)
		if err := c.Delete("k"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, found := get(t, c, "k"); found {
			t.Error("k found after Delete")
		}
	})
	// Deleting a key that does not exist is not an error.
	t.Run("missing key", func(t *testing.T) {
		if err := c.Delete("missing"); err != nil {
			t.Errorf("Delete(missing) = %v, want nil", err)
		}
	})
	// The store rejects empty keys.
	t.Run("empty key", func(t *testing.T) {
		if err := c.Delete(""); err == nil || !strings.HasPrefix(err.Error(), "cache delete") {
			t.Errorf("Delete(\"\") = %v, want a cache delete error", err)
		}
	})
}

// TestSetTTL checks the expiry the store records for a TTL.
func TestSetTTL(t *testing.T) {
	t.Parallel()
	c := open(t, t.TempDir())

	set(t, c, "forever", "v", 0)
	if exp := expiresAt(t, c, "forever"); exp != 0 {
		t.Errorf("expiry with ttl 0 = %d, want none", exp)
	}

	before := time.Now()
	set(t, c, "minute", "v", time.Minute)
	after := time.Now()
	// The store counts expiry in whole seconds, rounded down.
	lo, hi := uint64(before.Add(time.Minute).Unix()), uint64(after.Add(time.Minute).Unix())
	if exp := expiresAt(t, c, "minute"); exp < lo || exp > hi {
		t.Errorf("expiry with ttl 1m = %d, want between %d and %d", exp, lo, hi)
	}
}

// TestReopen checks that values survive closing and reopening the store.
func TestReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	set(t, c, "k", "v", 0)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	c = open(t, dir)
	if v, found := get(t, c, "k"); !found || v != "v" {
		t.Errorf("Get after reopen = %q, %v; want %q, true", v, found, "v")
	}
}

// TestOpenSameDir checks that a dir in use cannot be opened again.
func TestOpenSameDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	open(t, dir)

	if c, err := Open(dir); err == nil {
		c.Close()
		t.Fatal("second Open of the same dir succeeded, want a lock error")
	}
}

// TestOpenBadDir checks that opening a file fails with an error naming it.
func TestOpenBadDir(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Open(file)
	if err == nil {
		c.Close()
		t.Fatal("Open of a file succeeded, want an error")
	}
	if !strings.Contains(err.Error(), file) {
		t.Errorf("error %q does not name %q", err, file)
	}
}

// TestConcurrent checks concurrent Set, Get and Delete; run it with -race.
func TestConcurrent(t *testing.T) {
	t.Parallel()
	c := open(t, t.TempDir())

	g, ctx := errgroup.WithContext(t.Context())
	for gi := range 8 {
		g.Go(func() error {
			for i := range 100 {
				if ctx.Err() != nil {
					return nil // another goroutine failed
				}
				key := fmt.Sprintf("k%d", i%10)
				if err := c.Set(key, fmt.Append(nil, gi), time.Minute); err != nil {
					return fmt.Errorf("Set: %w", err)
				}
				if _, _, err := c.Get(key); err != nil {
					return fmt.Errorf("Get: %w", err)
				}
				if i%7 == 0 {
					if err := c.Delete(key); err != nil {
						return fmt.Errorf("Delete: %w", err)
					}
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}
