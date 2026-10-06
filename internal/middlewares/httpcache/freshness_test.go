package httpcache

import (
	"net/http"
	"testing"
	"time"
)

// TestParseDeltaSeconds checks delta-seconds parsing, including the 2^31 cap.
func TestParseDeltaSeconds(t *testing.T) {
	t.Parallel()
	const capped = maxDeltaSeconds * time.Second
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"zero", "0", 0},
		{"seconds", "60", time.Minute},
		{"empty", "", 0},
		{"text", "abc", 0},
		{"negative", "-1", 0},
		{"decimal", "1.5", 0},
		{"the cap itself", "2147483648", capped}, // 2^31
		{"above the cap", "2147483649", capped},
		{"overflows uint64", "18446744073709551616", capped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parseDeltaSeconds(tt.in); got != tt.want {
				t.Errorf("parseDeltaSeconds(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestFreshnessLifetime checks the lifetime from s-maxage, max-age or Expires minus Date.
func TestFreshnessLifetime(t *testing.T) {
	t.Parallel()
	const date = "Sat, 01 Jan 2000 00:00:00 GMT"
	tests := []struct {
		name   string
		header []string
		want   time.Duration
	}{
		{"none", nil, 0},
		{"max-age", []string{"Cache-Control", "max-age=60"}, time.Minute},
		{"s-maxage wins over max-age", []string{"Cache-Control", "max-age=60, s-maxage=10"}, 10 * time.Second},
		{"max-age wins over Expires", []string{"Cache-Control", "max-age=60", "Date", date, "Expires", "Sat, 01 Jan 2000 01:00:00 GMT"}, time.Minute},
		{"invalid max-age is 0, not Expires", []string{"Cache-Control", "max-age=x", "Date", date, "Expires", "Sat, 01 Jan 2000 01:00:00 GMT"}, 0},
		{"Expires minus Date", []string{"Date", date, "Expires", "Sat, 01 Jan 2000 00:01:30 GMT"}, 90 * time.Second},
		{"Expires before Date", []string{"Date", date, "Expires", "Fri, 31 Dec 1999 00:00:00 GMT"}, 0},
		{"invalid Expires", []string{"Date", date, "Expires", "0"}, 0},
		{"Expires without Date", []string{"Expires", "Sat, 01 Jan 2000 00:01:30 GMT"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := headerOf(tt.header...)
			if got := freshnessLifetime(h, parseCacheControl(h)); got != tt.want {
				t.Errorf("freshnessLifetime = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestInitialAge checks the age on arrival: Age plus the time the backend took.
func TestInitialAge(t *testing.T) {
	t.Parallel()
	requestTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	responseTime := requestTime.Add(2 * time.Second)
	tests := []struct {
		name string
		age  string
		want time.Duration
	}{
		{"no Age", "", 2 * time.Second},
		{"Age plus the backend's time", "10", 12 * time.Second},
		{"first of several values", "10, 20", 12 * time.Second},
		{"invalid Age", "abc", 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := headerOf("Age", tt.age)
			if got := initialAge(h, requestTime, responseTime); got != tt.want {
				t.Errorf("initialAge with Age %q = %v, want %v", tt.age, got, tt.want)
			}
		})
	}
}

// TestGetFreshness checks when a response was generated and when it goes stale.
func TestGetFreshness(t *testing.T) {
	t.Parallel()
	requestTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	responseTime := requestTime.Add(2 * time.Second)
	h := headerOf("Cache-Control", "max-age=60", "Age", "10")

	generatedAt, expiresAt := getFreshness(h, parseCacheControl(h), requestTime, responseTime)
	if want := responseTime.Add(-12 * time.Second); !generatedAt.Equal(want) {
		t.Errorf("generatedAt = %v, want %v", generatedAt, want)
	}
	if want := generatedAt.Add(time.Minute); !expiresAt.Equal(want) {
		t.Errorf("expiresAt = %v, want %v", expiresAt, want)
	}
}

// headerOf returns a header with the given name/value pairs; empty values
// are left out.
func headerOf(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			h.Add(pairs[i], pairs[i+1])
		}
	}
	return h
}
