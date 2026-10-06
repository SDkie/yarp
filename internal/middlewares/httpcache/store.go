package httpcache

import (
	"bytes"
	"context"
	"encoding/gob"
	"log/slog"
	"net/http"
	"time"
)

// Store keeps the encoded records. *cache.Cache implements it.
type Store interface {
	// Get returns the value under key; found is false if it is missing or
	// expired.
	Get(key string) (value []byte, found bool, err error)
	// Set stores value under key; it expires after ttl, or never if ttl is 0.
	Set(key string, value []byte, ttl time.Duration) error
	// Delete removes key; a missing key is not an error.
	Delete(key string) error
}

// record is a stored response, kept under its response key (see respPrefix).
type record struct {
	Key    string // plain variant key
	Status int
	Header http.Header
	Body   []byte

	// When the backend generated the response, as best yarp can tell, and
	// when it becomes stale (see getFreshness). The Age header of a hit is
	// the time since GeneratedAt.
	GeneratedAt time.Time
	ExpiresAt   time.Time
}

// varyRecord holds the Vary header names of the responses stored for a URL,
// kept under its Vary key (see varyPrefix). Names is empty when the response
// had no Vary header.
type varyRecord struct {
	Key   string // plain base key
	Names []string
}

// load decodes the value stored under key into v. Missing keys and read or
// decode failures (which are logged) report false.
func load(ctx context.Context, s Store, key string, v any) bool {
	data, found, err := s.Get(key)
	if err != nil {
		slog.ErrorContext(ctx, "cache read failed", "key", key, "error", err)
		return false
	}
	if !found {
		return false
	}
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(v); err != nil {
		slog.ErrorContext(ctx, "cache entry is corrupt", "key", key, "error", err)
		return false
	}
	return true
}

// store saves v under key until ttl passes and reports whether it was saved.
// Failures are logged; the response has already been sent, so they never
// affect the client.
func store(ctx context.Context, s Store, key string, v any, ttl time.Duration) bool {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		slog.ErrorContext(ctx, "cache encode failed", "key", key, "error", err)
		return false
	}
	if err := s.Set(key, buf.Bytes(), ttl); err != nil {
		slog.ErrorContext(ctx, "cache write failed", "key", key, "error", err)
		return false
	}
	return true
}
