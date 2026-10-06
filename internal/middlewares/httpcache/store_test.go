package httpcache

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// TestStoreAndLoad checks that a record survives store and load unchanged.
func TestStoreAndLoad(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newMemStore()
	want := record{
		Key:         "GET example.com/a",
		Status:      http.StatusOK,
		Header:      http.Header{"Content-Type": {"text/plain"}},
		Body:        []byte("body"),
		GeneratedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		ExpiresAt:   time.Date(2000, 1, 1, 0, 1, 0, 0, time.UTC),
	}
	if !store(ctx, s, "k", &want, time.Minute) {
		t.Fatal("store failed")
	}
	var got record
	if !load(ctx, s, "k", &got) || !reflect.DeepEqual(got, want) {
		t.Errorf("load = %+v, want %+v", got, want)
	}
}

// TestLoadFailures checks that load reports false for entries it cannot use.
func TestLoadFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A missing key.
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		var v varyRecord
		if load(ctx, newMemStore(), "missing", &v) {
			t.Error("load of a missing key succeeded")
		}
	})
	// An entry that is not valid gob.
	t.Run("corrupt", func(t *testing.T) {
		t.Parallel()
		s := newMemStore()
		s.Set("corrupt", []byte("garbage"), 0)
		var v varyRecord
		if load(ctx, s, "corrupt", &v) {
			t.Error("load of a corrupt entry succeeded")
		}
	})
	// The store's Get fails.
	t.Run("Get fails", func(t *testing.T) {
		t.Parallel()
		s := newMemStore()
		store(ctx, s, "k", &varyRecord{Key: "x"}, 0)
		s.fail(true, false, false)
		var v varyRecord
		if load(ctx, s, "k", &v) {
			t.Error("load succeeded although Get failed")
		}
	})
}

// TestStoreFailuresReported checks that store reports false when it cannot save.
func TestStoreFailuresReported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// gob cannot encode the value.
	t.Run("encode fails", func(t *testing.T) {
		t.Parallel()
		s := newMemStore()
		if store(ctx, s, "k", make(chan int), 0) {
			t.Error("store of a value gob cannot encode succeeded")
		}
		if n := s.len(); n != 0 {
			t.Errorf("store holds %d entries, want 0", n)
		}
	})
	// The store's Set fails.
	t.Run("Set fails", func(t *testing.T) {
		t.Parallel()
		s := newMemStore()
		s.fail(false, true, false)
		if store(ctx, s, "k", &varyRecord{}, 0) {
			t.Error("store succeeded although Set failed")
		}
		if n := s.len(); n != 0 {
			t.Errorf("store holds %d entries, want 0", n)
		}
	})
}
