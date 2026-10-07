package integration

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SDkie/yarp/internal/badgerdb"
)

// cacheConfig is a config file with one entry point, "web", and the cache on.
const cacheConfig = `
entryPoints:
  web:
    address: 127.0.0.1:0
providers:
  file:
    filename: routes.yml
cache:
  enabled: true
`

// Cache-Status values yarp sends (RFC 9211).
const (
	cacheHit    = "yarp; hit"
	cacheMiss   = "yarp; fwd=miss"
	cacheBypass = "yarp; fwd=bypass"
)

// cacheStore is a Badger store that reports each Set and Delete, so tests
// can wait for the cache's background writes.
type cacheStore struct {
	*badgerdb.Store
	sets    chan struct{}
	deletes chan struct{}
	closed  bool
}

// openStore opens a cacheStore in dir, closed when the test ends.
func openStore(t *testing.T, dir string) *cacheStore {
	t.Helper()
	db, err := badgerdb.Open(dir)
	if err != nil {
		t.Fatalf("badgerdb.Open: %v", err)
	}
	s := &cacheStore{Store: db, sets: make(chan struct{}, 64), deletes: make(chan struct{}, 64)}
	t.Cleanup(func() { s.close(t) })
	return s
}

func (s *cacheStore) Set(key string, value []byte, ttl time.Duration) error {
	err := s.Store.Set(key, value, ttl)
	s.sets <- struct{}{}
	return err
}

func (s *cacheStore) Delete(key string) error {
	err := s.Store.Delete(key)
	s.deletes <- struct{}{}
	return err
}

// close closes the store. Later calls do nothing.
func (s *cacheStore) close(t *testing.T) {
	t.Helper()
	if s.closed {
		return
	}
	s.closed = true
	if err := s.Store.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// waitSets waits for n more Set calls.
func (s *cacheStore) waitSets(t *testing.T, n int) {
	t.Helper()
	wait(t, s.sets, n, "Set")
}

// waitDeletes waits for n more Delete calls.
func (s *cacheStore) waitDeletes(t *testing.T, n int) {
	t.Helper()
	wait(t, s.deletes, n, "Delete")
}

func wait(t *testing.T, ch <-chan struct{}, n int, name string) {
	t.Helper()
	for i := range n {
		select {
		case <-ch:
		case <-time.After(waitTimeout):
			t.Fatalf("got %d %s calls, want %d", i, name, n)
		}
	}
}

// Writes per stored response: the response record, then the Vary record.
const setsPerResponse = 2

// newCountingBackend starts a backend that runs h and counts its requests,
// and returns its URL and the count.
func newCountingBackend(t *testing.T, h http.HandlerFunc) (string, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	backendURL := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		h(w, r)
	})
	return backendURL, &count
}

// cacheable answers with a body that may be cached for a minute.
func cacheable(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		io.WriteString(w, body)
	}
}

// fetch sends req and checks its Cache-Status and body.
func fetch(t *testing.T, req *http.Request, wantCacheStatus, wantBody string) *http.Response {
	t.Helper()
	resp, body := do(t, req)
	if got := resp.Header.Get("Cache-Status"); got != wantCacheStatus {
		t.Errorf("%s %s: Cache-Status = %q, want %q", req.Method, req.URL.Path, got, wantCacheStatus)
	}
	if body != wantBody {
		t.Errorf("%s %s: body = %q, want %q", req.Method, req.URL.Path, body, wantBody)
	}
	return resp
}

func checkCount(t *testing.T, count *atomic.Int32, want int32) {
	t.Helper()
	if got := count.Load(); got != want {
		t.Errorf("backend got %d requests, want %d", got, want)
	}
}

// TestCacheHit checks that a cacheable response is served from the cache
// the second time, without reaching the backend.
func TestCacheHit(t *testing.T) {
	t.Parallel()
	backendURL, count := newCountingBackend(t, cacheable("v1"))
	store := openStore(t, t.TempDir())
	y := startYarpWithStore(t, cacheConfig, appRoutes(backendURL), store)

	fetch(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""), cacheMiss, "v1")
	store.waitSets(t, setsPerResponse)
	resp := fetch(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""), cacheHit, "v1")

	if _, err := strconv.Atoi(resp.Header.Get("Age")); err != nil {
		t.Errorf("Age = %q, want a number", resp.Header.Get("Age"))
	}
	checkCount(t, count, 1)
}

// TestCacheBypass checks that a Range request skips a cached response.
func TestCacheBypass(t *testing.T) {
	t.Parallel()
	backendURL, count := newCountingBackend(t, cacheable("v1"))
	store := openStore(t, t.TempDir())
	y := startYarpWithStore(t, cacheConfig, appRoutes(backendURL), store)

	fetch(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""), cacheMiss, "v1")
	store.waitSets(t, setsPerResponse)
	req := newRequest(t, http.MethodGet, y.url("web", "/page"), "")
	req.Header.Set("Range", "bytes=0-0")
	fetch(t, req, cacheBypass, "v1")

	checkCount(t, count, 2)
}

// TestCacheInvalidation checks that a successful POST drops the cached GET
// for its URL.
func TestCacheInvalidation(t *testing.T) {
	t.Parallel()
	var version atomic.Int32
	backendURL, count := newCountingBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			version.Add(1)
			return
		}
		cacheable(fmt.Sprintf("v%d", version.Load()))(w, r)
	})
	store := openStore(t, t.TempDir())
	y := startYarpWithStore(t, cacheConfig, appRoutes(backendURL), store)

	fetch(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""), cacheMiss, "v0")
	store.waitSets(t, setsPerResponse)
	fetch(t, newRequest(t, http.MethodPost, y.url("web", "/page"), "update"), cacheBypass, "")
	store.waitDeletes(t, 2) // The GET and HEAD Vary records.
	fetch(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""), cacheMiss, "v1")

	checkCount(t, count, 3)
}

// TestCacheVary checks that responses with Vary are stored per value of the
// named request header.
func TestCacheVary(t *testing.T) {
	t.Parallel()
	backendURL, count := newCountingBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Language")
		cacheable(r.Header.Get("Accept-Language"))(w, r)
	})
	store := openStore(t, t.TempDir())
	y := startYarpWithStore(t, cacheConfig, appRoutes(backendURL), store)

	get := func(lang, wantCacheStatus string) {
		t.Helper()
		req := newRequest(t, http.MethodGet, y.url("web", "/page"), "")
		req.Header.Set("Accept-Language", lang)
		fetch(t, req, wantCacheStatus, lang)
	}
	get("en", cacheMiss)
	store.waitSets(t, setsPerResponse)
	get("fr", cacheMiss)
	store.waitSets(t, setsPerResponse)
	get("en", cacheHit)
	get("fr", cacheHit)

	checkCount(t, count, 2)
}

// TestCacheKeyedByHost checks that the same path on two hosts is cached
// separately, so one host's response never reaches the other's clients.
func TestCacheKeyedByHost(t *testing.T) {
	t.Parallel()
	routes := fmt.Sprintf(`
routes:
  a:
    host: a.test
    entryPoints: [web]
    servers: [{url: %s}]
  b:
    host: b.test
    entryPoints: [web]
    servers: [{url: %s}]
`,
		newBackend(t, cacheable("a")),
		newBackend(t, cacheable("b")),
	)
	store := openStore(t, t.TempDir())
	y := startYarpWithStore(t, cacheConfig, routes, store)

	get := func(host, wantCacheStatus string) {
		t.Helper()
		req := newRequest(t, http.MethodGet, y.url("web", "/page"), "")
		req.Host = host
		fetch(t, req, wantCacheStatus, host[:1])
	}
	get("a.test", cacheMiss)
	store.waitSets(t, setsPerResponse)
	get("b.test", cacheMiss)
	store.waitSets(t, setsPerResponse)
	get("a.test", cacheHit)
	get("b.test", cacheHit)
}

// TestCacheSurvivesRestart checks that a response cached before a restart is
// served from the same store after it.
func TestCacheSurvivesRestart(t *testing.T) {
	t.Parallel()
	backendURL, count := newCountingBackend(t, cacheable("v1"))
	dir := t.TempDir()
	// The restarted yarp gets a new port, and the cache key holds the Host.
	newPageRequest := func(y *yarp) *http.Request {
		req := newRequest(t, http.MethodGet, y.url("web", "/page"), "")
		req.Host = "app.test"
		return req
	}

	store := openStore(t, dir)
	y := startYarpWithStore(t, cacheConfig, appRoutes(backendURL), store)
	fetch(t, newPageRequest(y), cacheMiss, "v1")
	store.waitSets(t, setsPerResponse)
	y.stop(t)
	store.close(t)

	store = openStore(t, dir)
	y = startYarpWithStore(t, cacheConfig, appRoutes(backendURL), store)
	fetch(t, newPageRequest(y), cacheHit, "v1")

	checkCount(t, count, 1)
}

// TestCacheDisabled checks that with the cache off, responses are neither
// cached nor marked with Cache-Status.
func TestCacheDisabled(t *testing.T) {
	t.Parallel()
	backendURL, count := newCountingBackend(t, cacheable("v1"))
	y := startYarp(t, webConfig, appRoutes(backendURL))

	fetch(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""), "", "v1")
	fetch(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""), "", "v1")

	checkCount(t, count, 2)
}
