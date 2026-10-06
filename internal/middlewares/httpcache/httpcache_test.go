package httpcache

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Inside a synctest bubble the clock starts at midnight UTC, 2000-01-01.
const bubbleStart = "Sat, 01 Jan 2000 00:00:00 GMT"

var errStore = errors.New("store failure")

// memStore is an in-memory Store. failGet, failSet and failDelete make the
// matching method fail; failSetPrefix fails only Sets of keys with that
// prefix. setKeys lists the key of every Set call, in order.
type memStore struct {
	mu                           sync.Mutex
	m                            map[string][]byte
	failGet, failSet, failDelete bool
	failSetPrefix                string
	setKeys                      []string
}

func newMemStore() *memStore {
	return &memStore{m: map[string][]byte{}}
}

func (s *memStore) Get(key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGet {
		return nil, false, errStore
	}
	v, ok := s.m[key]
	return v, ok, nil
}

func (s *memStore) Set(key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setKeys = append(s.setKeys, key)
	if s.failSet || (s.failSetPrefix != "" && strings.HasPrefix(key, s.failSetPrefix)) {
		return errStore
	}
	s.m[key] = value
	return nil
}

func (s *memStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDelete {
		return errStore
	}
	delete(s.m, key)
	return nil
}

func (s *memStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// fail sets which methods fail.
func (s *memStore) fail(get, set, del bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failGet, s.failSet, s.failDelete = get, set, del
}

// respond returns a backend that answers with status, the given header
// name/value pairs and a body naming the request.
func respond(status int, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Add(header[i], header[i+1])
		}
		w.WriteHeader(status)
		io.WriteString(w, r.Method+" "+r.URL.RequestURI())
	}
}

// cacheTest is the cache middleware in front of a counting backend. It must
// be used inside a synctest bubble.
type cacheTest struct {
	t       *testing.T
	h       http.Handler
	store   *memStore
	backend http.HandlerFunc
	calls   int // backend calls
}

func newCacheTest(t *testing.T, backend http.HandlerFunc) *cacheTest {
	ct := &cacheTest{t: t, store: newMemStore(), backend: backend}
	ct.h = New(ct.store, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct.calls++
		ct.backend(w, r)
	}))
	return ct
}

// do sends a request through the cache, with the given header name/value
// pairs, and waits for any background save. target defaults to host
// example.com.
func (ct *cacheTest) do(method, target string, header ...string) *http.Response {
	ct.t.Helper()
	if !strings.HasPrefix(target, "http") {
		target = "http://example.com" + target
	}
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Add(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	ct.h.ServeHTTP(w, r)
	synctest.Wait()
	return w.Result()
}

// expect checks a response's status, Cache-Status and the backend calls so
// far.
func (ct *cacheTest) expect(resp *http.Response, status int, cacheStatus string, calls int) {
	ct.t.Helper()
	got := strings.Join(resp.Header.Values("Cache-Status"), ", ")
	if resp.StatusCode != status || got != cacheStatus || ct.calls != calls {
		ct.t.Errorf("got %d, Cache-Status %q, %d backend calls; want %d, %q, %d",
			resp.StatusCode, got, ct.calls, status, cacheStatus, calls)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	miss   = "yarp; fwd=miss"
	hit    = "yarp; hit"
	bypass = "yarp; fwd=bypass"
)

// TestMissThenHit checks that a miss is stored, then served as a hit with Age.
func TestMissThenHit(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60", "ETag", `"v1"`))

		resp := ct.do("GET", "/a")
		ct.expect(resp, http.StatusOK, miss, 1)
		if body := readBody(t, resp); body != "GET /a" {
			t.Errorf("miss body = %q", body)
		}

		resp = ct.do("GET", "/a")
		ct.expect(resp, http.StatusOK, hit, 1)
		if body := readBody(t, resp); body != "GET /a" {
			t.Errorf("hit body = %q", body)
		}
		if got := resp.Header.Get("ETag"); got != `"v1"` {
			t.Errorf("hit ETag = %q", got)
		}
		if got := resp.Header.Get("Age"); got != "0" {
			t.Errorf("hit Age = %q, want 0", got)
		}

		time.Sleep(30 * time.Second)
		resp = ct.do("GET", "/a")
		ct.expect(resp, http.StatusOK, hit, 1)
		if got := resp.Header.Get("Age"); got != "30" {
			t.Errorf("Age after 30s = %q, want 30", got)
		}
	})
}

// TestStaleIsFetchedAgain checks that a stale response is fetched and stored again.
func TestStaleIsFetchedAgain(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60"))
		ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 1)

		time.Sleep(59 * time.Second)
		ct.expect(ct.do("GET", "/a"), http.StatusOK, hit, 1)

		time.Sleep(time.Second) // now exactly 60s old: stale
		ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 2)
		resp := ct.do("GET", "/a")
		ct.expect(resp, http.StatusOK, hit, 2)
		if got := resp.Header.Get("Age"); got != "0" {
			t.Errorf("Age of the new copy = %q, want 0", got)
		}
	})
}

// TestHeadAndGetStoredSeparately checks that HEAD and GET responses are cached separately.
func TestHeadAndGetStoredSeparately(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60"))

		ct.expect(ct.do("HEAD", "/a"), http.StatusOK, miss, 1)
		resp := ct.do("HEAD", "/a")
		ct.expect(resp, http.StatusOK, hit, 1)
		if body := readBody(t, resp); body != "" {
			t.Errorf("HEAD hit body = %q, want none", body)
		}
		ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 2)
		resp = ct.do("GET", "/a")
		ct.expect(resp, http.StatusOK, hit, 2)
		if body := readBody(t, resp); body != "GET /a" {
			t.Errorf("GET hit body = %q", body)
		}
	})
}

// TestConditionalRequests checks 304 or 200 answers to If-None-Match and If-Modified-Since.
func TestConditionalRequests(t *testing.T) {
	t.Parallel()
	const lastModified = "Fri, 31 Dec 1999 00:00:00 GMT"
	tests := []struct {
		name   string
		header []string
		want   int
	}{
		{"If-None-Match strong matches weak", []string{"If-None-Match", `"v1"`}, http.StatusNotModified},
		{"If-None-Match list", []string{"If-None-Match", `"x", W/"v1"`}, http.StatusNotModified},
		{"If-None-Match lines", []string{"If-None-Match", `"x"`, "If-None-Match", `W/"v1"`}, http.StatusNotModified},
		{"If-None-Match star", []string{"If-None-Match", "*"}, http.StatusNotModified},
		{"If-None-Match other", []string{"If-None-Match", `"x"`}, http.StatusOK},
		{"If-None-Match wins over If-Modified-Since", []string{"If-None-Match", `"x"`, "If-Modified-Since", bubbleStart}, http.StatusOK},
		{"If-Modified-Since equal", []string{"If-Modified-Since", lastModified}, http.StatusNotModified},
		{"If-Modified-Since later", []string{"If-Modified-Since", bubbleStart}, http.StatusNotModified},
		{"If-Modified-Since earlier", []string{"If-Modified-Since", "Thu, 30 Dec 1999 00:00:00 GMT"}, http.StatusOK},
		{"If-Modified-Since invalid", []string{"If-Modified-Since", "yesterday"}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ct := newCacheTest(t, respond(http.StatusOK,
					"Cache-Control", "max-age=60",
					"ETag", `W/"v1"`,
					"Last-Modified", lastModified,
					"Content-Type", "text/plain",
					"X-Extra", "1",
				))
				ct.do("GET", "/a")

				resp := ct.do("GET", "/a", tt.header...)
				ct.expect(resp, tt.want, hit, 1)
				if tt.want != http.StatusNotModified {
					return
				}
				// A 304 carries only the stored fields RFC 9110 lists.
				if body := readBody(t, resp); body != "" {
					t.Errorf("304 body = %q", body)
				}
				for _, name := range []string{"Cache-Control", "ETag", "Last-Modified", "Date", "Age"} {
					if resp.Header.Get(name) == "" {
						t.Errorf("304 lacks %s", name)
					}
				}
				for _, name := range []string{"Content-Type", "X-Extra"} {
					if got := resp.Header.Get(name); got != "" {
						t.Errorf("304 has %s: %q", name, got)
					}
				}
			})
		})
	}
}

// TestConditionalRequestOnNon200 checks that only a stored 200 is answered with 304.
func TestConditionalRequestOnNon200(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ct := newCacheTest(t, respond(http.StatusMovedPermanently, "Cache-Control", "max-age=60", "ETag", `"v1"`, "Location", "/b"))
		ct.do("GET", "/a")
		resp := ct.do("GET", "/a", "If-None-Match", `"v1"`)
		ct.expect(resp, http.StatusMovedPermanently, hit, 1)
	})
}

// TestBypass checks that bypassing requests neither store nor invalidate.
func TestBypass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method string
		header []string
	}{
		{"OPTIONS", nil},
		{"TRACE", nil},
		{"GET", []string{"Range", "bytes=0-1"}},
		{"GET", []string{"If-Match", `"v1"`}},
		{"GET", []string{"If-Unmodified-Since", bubbleStart}},
		{"GET", []string{"If-Range", `"v1"`}},
		{"HEAD", []string{"Range", "bytes=0-1"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(append([]string{tt.method}, tt.header...), " "), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60"))
				ct.do("GET", "/a") // stored

				ct.expect(ct.do(tt.method, "/a", tt.header...), http.StatusOK, bypass, 2)
				ct.expect(ct.do(tt.method, "/a", tt.header...), http.StatusOK, bypass, 3)
				// Neither stored nor invalidated anything.
				ct.expect(ct.do("GET", "/a"), http.StatusOK, hit, 3)
			})
		})
	}
}

// TestUnsafeMethodsInvalidate checks which unsafe requests invalidate their URL.
func TestUnsafeMethodsInvalidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method      string
		status      int
		invalidates bool
	}{
		{"POST", http.StatusOK, true},
		{"POST", http.StatusCreated, true},
		{"POST", http.StatusNoContent, true},
		{"POST", http.StatusMovedPermanently, true},
		{"POST", http.StatusSeeOther, true},
		{"POST", 399, true}, // the last 3xx; it has no named constant
		{"POST", http.StatusBadRequest, false},
		{"POST", http.StatusNotFound, false},
		{"POST", http.StatusInternalServerError, false},
		{"POST", http.StatusServiceUnavailable, false},
		{"PUT", http.StatusOK, true},
		{"PATCH", http.StatusOK, true},
		{"DELETE", http.StatusNoContent, true},
		{"PURGE", http.StatusOK, true}, // unknown methods count as unsafe
		{"DELETE", http.StatusNotFound, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s %d", tt.method, tt.status), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ct := newCacheTest(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "GET" || r.Method == "HEAD" {
						respond(http.StatusOK, "Cache-Control", "max-age=60")(w, r)
						return
					}
					w.WriteHeader(tt.status)
				})
				ct.do("GET", "/a")
				ct.do("HEAD", "/a")
				ct.do("GET", "/a?x=1")
				ct.do("GET", "http://other.com/a")

				ct.expect(ct.do(tt.method, "/a"), tt.status, bypass, 5)

				want, calls := hit, 5
				if tt.invalidates {
					want, calls = miss, 6
				}
				ct.expect(ct.do("GET", "/a"), http.StatusOK, want, calls)
				if tt.invalidates {
					calls++
				}
				ct.expect(ct.do("HEAD", "/a"), http.StatusOK, want, calls)
				// Only that URL: other queries and hosts are kept.
				ct.expect(ct.do("GET", "/a?x=1"), http.StatusOK, hit, calls)
				ct.expect(ct.do("GET", "http://other.com/a"), http.StatusOK, hit, calls)
			})
		})
	}
}

// TestNeverStored checks responses that must never be stored.
func TestNeverStored(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		header []string
	}{
		{"no-store", http.StatusOK, []string{"Cache-Control", "no-store, max-age=60"}},
		{"private", http.StatusOK, []string{"Cache-Control", "private, max-age=60"}},
		{"no-cache", http.StatusOK, []string{"Cache-Control", "no-cache, max-age=60"}},
		{"Set-Cookie", http.StatusOK, []string{"Cache-Control", "max-age=60", "Set-Cookie", "a=b"}},
		{"500", http.StatusInternalServerError, []string{"Cache-Control", "max-age=60"}},
		{"503", http.StatusServiceUnavailable, []string{"Cache-Control", "max-age=60"}},
		{"206", http.StatusPartialContent, []string{"Cache-Control", "max-age=60"}},
		{"304", http.StatusNotModified, []string{"Cache-Control", "max-age=60"}},
		{"no freshness", http.StatusOK, nil},
		{"max-age=0", http.StatusOK, []string{"Cache-Control", "max-age=0"}},
		{"invalid max-age", http.StatusOK, []string{"Cache-Control", "max-age=soon"}},
		{"Vary *", http.StatusOK, []string{"Cache-Control", "max-age=60", "Vary", "*"}},
		{"Vary list with *", http.StatusOK, []string{"Cache-Control", "max-age=60", "Vary", "Accept, *"}},
		{"event stream", http.StatusOK, []string{"Cache-Control", "max-age=60", "Content-Type", "text/event-stream; charset=utf-8"}},
		{"Expires before Date", http.StatusOK, []string{"Date", bubbleStart, "Expires", "Fri, 31 Dec 1999 00:00:00 GMT"}},
		{"Expires invalid", http.StatusOK, []string{"Date", bubbleStart, "Expires", "0"}},
		{"Age past max-age", http.StatusOK, []string{"Cache-Control", "max-age=60", "Age", "60"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ct := newCacheTest(t, respond(tt.status, tt.header...))
				ct.expect(ct.do("GET", "/a"), tt.status, miss, 1)
				ct.expect(ct.do("GET", "/a"), tt.status, miss, 2)
				if n := ct.store.len(); n != 0 {
					t.Errorf("store holds %d entries, want 0", n)
				}
			})
		})
	}
}

// TestStored checks responses that are stored and served as hits, with their Age.
func TestStored(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		header  []string
		wantAge string
	}{
		{"max-age", http.StatusOK, []string{"Cache-Control", "max-age=60"}, "0"},
		{"s-maxage wins over max-age", http.StatusOK, []string{"Cache-Control", "s-maxage=60, max-age=0"}, "0"},
		{"quoted and upper case", http.StatusOK, []string{"Cache-Control", `MAX-AGE="60"`}, "0"},
		{"Expires", http.StatusOK, []string{"Date", bubbleStart, "Expires", "Sat, 01 Jan 2000 00:01:00 GMT"}, "0"},
		// Without Date, the time the response arrived is used (RFC 9110 section 6.6.1).
		{"Expires without Date", http.StatusOK, []string{"Expires", "Sat, 01 Jan 2000 00:01:00 GMT"}, "0"},
		{"Age below max-age", http.StatusOK, []string{"Cache-Control", "max-age=60", "Age", "30"}, "30"},
		{"404", http.StatusNotFound, []string{"Cache-Control", "max-age=60"}, "0"},
		{"301", http.StatusMovedPermanently, []string{"Cache-Control", "max-age=60", "Location", "/b"}, "0"},
		{"public", http.StatusOK, []string{"Cache-Control", "public, max-age=60"}, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ct := newCacheTest(t, respond(tt.status, tt.header...))
				ct.expect(ct.do("GET", "/a"), tt.status, miss, 1)
				resp := ct.do("GET", "/a")
				ct.expect(resp, tt.status, hit, 1)
				if got := resp.Header.Get("Age"); got != tt.wantAge {
					t.Errorf("Age = %q, want %q", got, tt.wantAge)
				}
			})
		})
	}
}

// TestAuthorization checks when responses to requests with Authorization are stored.
func TestAuthorization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cacheControl string
		stored       bool
	}{
		{"max-age=60", false},
		{"public, max-age=60", true},
		{"s-maxage=60", true},
		{"must-revalidate, max-age=60", true},
	}
	for _, tt := range tests {
		t.Run(tt.cacheControl, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", tt.cacheControl))
				ct.do("GET", "/a", "Authorization", "Bearer x")
				want, calls := miss, 2
				if tt.stored {
					want, calls = hit, 1
				}
				ct.expect(ct.do("GET", "/a", "Authorization", "Bearer x"), http.StatusOK, want, calls)
			})
		})
	}
}

// TestVary checks that each Vary variant is cached separately.
func TestVary(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ct := newCacheTest(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "max-age=60")
			w.Header().Add("Vary", "accept-encoding")
			w.Header().Add("Vary", "Accept-Encoding, X-Lang")
			io.WriteString(w, "for "+strings.Join(r.Header.Values("Accept-Encoding"), "|"))
		})
		steps := []struct {
			header []string
			want   string
			calls  int
			body   string
		}{
			{[]string{"Accept-Encoding", "gzip"}, miss, 1, "for gzip"},
			{[]string{"Accept-Encoding", "gzip"}, hit, 1, "for gzip"},
			{[]string{"Accept-Encoding", "br"}, miss, 2, "for br"},
			{[]string{"Accept-Encoding", "gzip"}, hit, 2, "for gzip"},
			{nil, miss, 3, "for "},
			{nil, hit, 3, "for "},
			{[]string{"Accept-Encoding", "gzip , br"}, miss, 4, "for gzip , br"},
			// Equivalent values (RFC 9111 section 4.1) share the copy.
			{[]string{"Accept-Encoding", "gzip,br"}, hit, 4, "for gzip , br"},
			{[]string{"Accept-Encoding", "gzip", "Accept-Encoding", "br"}, hit, 4, "for gzip , br"},
			{[]string{"Accept-Encoding", "gzip", "X-Lang", "en"}, miss, 5, "for gzip"},
			{[]string{"Accept-Encoding", "gzip"}, hit, 5, "for gzip"},
		}
		// The steps build on each other, so they cannot be subtests.
		for i, s := range steps {
			resp := ct.do("GET", "/a", s.header...)
			got := strings.Join(resp.Header.Values("Cache-Status"), ", ")
			if resp.StatusCode != http.StatusOK || got != s.want || ct.calls != s.calls {
				t.Errorf("step %d %q: got %d, Cache-Status %q, %d backend calls; want 200, %q, %d",
					i, s.header, resp.StatusCode, got, ct.calls, s.want, s.calls)
			}
			if body := readBody(t, resp); body != s.body {
				t.Errorf("step %d %q: body %q, want %q", i, s.header, body, s.body)
			}
		}
	})
}

// TestStoredDate checks how a missing or invalid Date is fixed in the stored copy.
func TestStoredDate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		date string // sent by the backend
		want string // on hits
	}{
		{"missing is added", "", bubbleStart},
		{"invalid is replaced", "nonsense", bubbleStart},
		{"valid is kept", "Fri, 31 Dec 1999 23:59:00 GMT", "Fri, 31 Dec 1999 23:59:00 GMT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				header := []string{"Cache-Control", "max-age=600"}
				if tt.date != "" {
					header = append(header, "Date", tt.date)
				}
				ct := newCacheTest(t, respond(http.StatusOK, header...))
				resp := ct.do("GET", "/a")
				if got := resp.Header.Get("Date"); got != tt.date {
					t.Errorf("miss Date = %q, want the backend's %q", got, tt.date)
				}
				time.Sleep(10 * time.Second)
				resp = ct.do("GET", "/a")
				ct.expect(resp, http.StatusOK, hit, 1)
				if got := resp.Header.Get("Date"); got != tt.want {
					t.Errorf("hit Date = %q, want %q", got, tt.want)
				}
			})
		})
	}
}

// TestBackendCacheStatusKept checks that the backend's Cache-Status comes before yarp's.
func TestBackendCacheStatusKept(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60", "Cache-Status", "upstream; hit"))
		ct.expect(ct.do("GET", "/a"), http.StatusOK, "upstream; hit, "+miss, 1)
		ct.expect(ct.do("GET", "/a"), http.StatusOK, "upstream; hit, "+hit, 1)
		ct.expect(ct.do("OPTIONS", "/a"), http.StatusOK, "upstream; hit, "+bypass, 2)
	})
}

// TestKeyIncludesHostAndQuery checks that host and query select separate entries.
func TestKeyIncludesHostAndQuery(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60"))
		ct.expect(ct.do("GET", "/a?x=1"), http.StatusOK, miss, 1)
		ct.expect(ct.do("GET", "/a?x=1"), http.StatusOK, hit, 1)
		ct.expect(ct.do("GET", "/a?x=2"), http.StatusOK, miss, 2)
		ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 3)
		ct.expect(ct.do("GET", "http://other.com/a?x=1"), http.StatusOK, miss, 4)
		ct.expect(ct.do("GET", "http://other.com/a?x=1"), http.StatusOK, hit, 4)
	})
}

// TestStoreFailures checks that store failures never break the response.
func TestStoreFailures(t *testing.T) {
	t.Parallel()
	// Failing reads make every request a miss.
	t.Run("reads fail", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60"))
			ct.store.fail(true, false, false)
			ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 1)
			ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 2)
		})
	})
	// Failing writes store nothing.
	t.Run("writes fail", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60"))
			ct.store.fail(false, true, false)
			ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 1)
			ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 2)
		})
	})
	// Failing deletes still answer the client.
	t.Run("deletes fail", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60"))
			ct.store.fail(false, false, true)
			ct.expect(ct.do("POST", "/a"), http.StatusOK, bypass, 1)
		})
	})
}

// TestUnreadableEntriesAreMisses checks that unreadable stored entries are misses.
func TestUnreadableEntriesAreMisses(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("GET", "http://example.com/a", nil)
	varyKey, _ := getVaryKey("GET", r)
	respKey, respPlain := getRespKey(r, nil)

	tests := []struct {
		name    string
		corrupt func(s *memStore)
	}{
		{"corrupt Vary record", func(s *memStore) {
			s.Set(varyKey, []byte("garbage"), 0)
		}},
		{"Vary record for another URL", func(s *memStore) {
			store(context.Background(), s, varyKey, &varyRecord{Key: "GET example.com/other"}, 0)
		}},
		{"corrupt response record", func(s *memStore) {
			s.Set(respKey, []byte("garbage"), 0)
		}},
		{"response record for another variant", func(s *memStore) {
			store(context.Background(), s, respKey, &record{Key: respPlain + "\nX absent", Status: http.StatusOK,
				ExpiresAt: time.Now().Add(time.Hour)}, 0)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ct := newCacheTest(t, respond(http.StatusOK, "Cache-Control", "max-age=60"))
				ct.do("GET", "/a")
				tt.corrupt(ct.store)

				ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 2)
				// The miss stores a good copy again.
				ct.expect(ct.do("GET", "/a"), http.StatusOK, hit, 2)
			})
		})
	}
}

// hijackWriter is a ResponseWriter whose connection can be hijacked.
type hijackWriter struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	client, server := net.Pipe()
	client.Close()
	w.hijacked = true
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

// TestAbortedResponseNotStored checks that a response aborted mid-body is not stored.
func TestAbortedResponseNotStored(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store := newMemStore()
		calls := 0
		h := New(store, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			// What the proxy does when the backend fails mid-body.
			w.Header().Set("Cache-Control", "max-age=60")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "part")
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				conn.Close()
			}
		}))
		for range 2 {
			w := &hijackWriter{ResponseRecorder: httptest.NewRecorder()}
			h.ServeHTTP(w, httptest.NewRequest("GET", "http://example.com/a", nil))
			synctest.Wait()
			if !w.hijacked {
				t.Fatal("the connection was not hijacked")
			}
		}
		if calls != 2 || store.len() != 0 {
			t.Errorf("%d backend calls, %d stored entries; want 2, 0", calls, store.len())
		}
	})
}

// TestStoredCopyIsIndependent checks that the stored body does not change with the backend's buffer.
func TestStoredCopyIsIndependent(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var backendBody bytes.Buffer
		backendBody.WriteString("original")
		ct := newCacheTest(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "max-age=60")
			w.Write(backendBody.Bytes())
		})
		ct.do("GET", "/a")
		backendBody.Reset()
		backendBody.WriteString("CHANGED!")

		resp := ct.do("GET", "/a")
		ct.expect(resp, http.StatusOK, hit, 1)
		if body := readBody(t, resp); body != "original" {
			t.Errorf("hit body = %q, want %q", body, "original")
		}
	})
}

// TestStaleBeforeStored checks that a response going stale while its body streams is not stored.
func TestStaleBeforeStored(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ct := newCacheTest(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "max-age=1")
			w.WriteHeader(http.StatusOK)
			time.Sleep(2 * time.Second)
			io.WriteString(w, "slow body")
		})
		ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 1)
		if n := ct.store.len(); n != 0 {
			t.Errorf("store holds %d entries, want 0", n)
		}
	})
}
