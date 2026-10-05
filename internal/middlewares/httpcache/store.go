package httpcache

import "time"

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
