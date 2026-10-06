package httpcache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

// recorded returns a recorder holding a stored 200 that varies on
// Accept-Encoding.
func recorded() *recorder {
	rec := &recorder{
		status: http.StatusOK,
		header: http.Header{"Cache-Control": {"max-age=60"}, "Vary": {"Accept-Encoding"}},
	}
	rec.body.WriteString("body")
	return rec
}

// TestSave checks what save writes to the store, and in which order.
func TestSave(t *testing.T) {
	t.Parallel()
	newRequest := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/a", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		return r
	}

	// The response record is written first, then the Vary record.
	t.Run("response then Vary record", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			s := newMemStore()
			h := &handler{s: s}
			r := newRequest()
			generatedAt, expiresAt := time.Now(), time.Now().Add(time.Minute)

			h.save(r, recorded(), generatedAt, expiresAt)
			synctest.Wait()

			respKey, respPlain := getRespKey(r, []string{"Accept-Encoding"})
			varyKey, varyPlain := getVaryKey(http.MethodGet, r)
			if want := []string{respKey, varyKey}; !slices.Equal(s.setKeys, want) {
				t.Fatalf("Set keys = %q, want %q", s.setKeys, want)
			}
			var e record
			if !load(context.Background(), s, respKey, &e) || e.Key != respPlain || e.Status != http.StatusOK ||
				string(e.Body) != "body" || !e.GeneratedAt.Equal(generatedAt) || !e.ExpiresAt.Equal(expiresAt) {
				t.Errorf("response record = %+v", e)
			}
			var v varyRecord
			if !load(context.Background(), s, varyKey, &v) || v.Key != varyPlain || !slices.Equal(v.Names, []string{"Accept-Encoding"}) {
				t.Errorf("Vary record = %+v", v)
			}
		})
	})

	// A failed response record leaves no Vary record pointing to nothing.
	t.Run("Vary record skipped when the response record fails", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			s := newMemStore()
			s.failSetPrefix = respPrefix
			r := newRequest()

			(&handler{s: s}).save(r, recorded(), time.Now(), time.Now().Add(time.Minute))
			synctest.Wait()

			respKey, _ := getRespKey(r, []string{"Accept-Encoding"})
			if want := []string{respKey}; !slices.Equal(s.setKeys, want) {
				t.Errorf("Set keys = %q, want only the response record %q", s.setKeys, want)
			}
			if n := s.len(); n != 0 {
				t.Errorf("store holds %d entries, want 0", n)
			}
		})
	})

	// A response that is already stale is not written at all.
	t.Run("nothing once stale", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			s := newMemStore()
			(&handler{s: s}).save(newRequest(), recorded(), time.Now().Add(-time.Minute), time.Now())
			synctest.Wait()

			if len(s.setKeys) != 0 {
				t.Errorf("Set keys = %q, want none", s.setKeys)
			}
		})
	})
}

// TestServeMiss checks that a slow backend's time counts as age, from the
// moment the request was forwarded.
func TestServeMiss(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ct := newCacheTest(t, func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(5 * time.Second)
			respond(http.StatusOK, "Cache-Control", "max-age=60")(w, r)
		})
		ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 1) // takes 5s

		resp := ct.do("GET", "/a")
		ct.expect(resp, http.StatusOK, hit, 1)
		if got := resp.Header.Get("Age"); got != "5" {
			t.Errorf("Age right after the miss = %q, want 5", got)
		}
		// The time the headers arrived.
		if got := resp.Header.Get("Date"); got != "Sat, 01 Jan 2000 00:00:05 GMT" {
			t.Errorf("Date = %q, want the arrival time 00:00:05", got)
		}

		time.Sleep(54 * time.Second) // 59s after the first request was sent
		resp = ct.do("GET", "/a")
		ct.expect(resp, http.StatusOK, hit, 1)
		if got := resp.Header.Get("Age"); got != "59" {
			t.Errorf("Age at 59s = %q, want 59", got)
		}

		time.Sleep(time.Second) // 60s: stale, though the response arrived at 5s
		ct.expect(ct.do("GET", "/a"), http.StatusOK, miss, 2)
	})
}
