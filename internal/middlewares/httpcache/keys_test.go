package httpcache

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestGetVaryKey checks the Vary record's key and the plain key it hashes.
func TestGetVaryKey(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("GET", "http://example.com/products?page=2", nil)

	key, plain := getVaryKey("GET", r)
	if want := "GET example.com/products?page=2"; plain != want {
		t.Errorf("plain = %q, want %q", plain, want)
	}
	if want := "vary:" + sha(plain); key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
	if headKey, _ := getVaryKey("HEAD", r); headKey == key {
		t.Error("GET and HEAD share a Vary key")
	}
}

// TestGetRespKey checks the response key built from the request's Vary header values.
func TestGetRespKey(t *testing.T) {
	t.Parallel()
	const base = "GET example.com/products?page=2"
	tests := []struct {
		name      string
		header    []string
		varyNames []string // from the response's Vary header
		want      string
	}{
		{"no Vary", nil, nil, base},
		{"one header", []string{"Accept-Encoding", "gzip"}, []string{"Accept-Encoding"}, base + "\nAccept-Encoding: gzip"},
		{"spaces removed", []string{"Accept-Encoding", "gzip , br"}, []string{"Accept-Encoding"}, base + "\nAccept-Encoding: gzip,br"},
		{"lines joined", []string{"Accept-Encoding", "gzip", "Accept-Encoding", "br"}, []string{"Accept-Encoding"}, base + "\nAccept-Encoding: gzip,br"},
		{"absent header", nil, []string{"Accept-Encoding"}, base + "\nAccept-Encoding absent"},
		{"two headers", []string{"X-Lang", "en"}, []string{"Accept-Encoding", "X-Lang"}, base + "\nAccept-Encoding absent\nX-Lang: en"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest("GET", "http://example.com/products?page=2", nil)
			for i := 0; i+1 < len(tt.header); i += 2 {
				r.Header.Add(tt.header[i], tt.header[i+1])
			}
			key, plain := getRespKey(r, tt.varyNames)
			if plain != tt.want {
				t.Errorf("plain = %q, want %q", plain, tt.want)
			}
			if want := "resp:" + sha(tt.want); key != want {
				t.Errorf("key = %q, want %q", key, want)
			}
		})
	}
}

// TestGetHashKey checks that hashed keys have a fixed length and differ.
func TestGetHashKey(t *testing.T) {
	t.Parallel()
	a, b := getHashKey("p:", strings.Repeat("a", 100_000)), getHashKey("p:", "b")
	if len(a) != len("p:")+64 || len(b) != len("p:")+64 {
		t.Errorf("key lengths %d and %d, want %d", len(a), len(b), len("p:")+64)
	}
	if a == b {
		t.Error("different plain keys hash to the same key")
	}
}

// TestGetVaryNames checks parsing of the response's Vary header.
func TestGetVaryNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		lines  []string
		want   []string
		wantOK bool
	}{
		{"none", nil, nil, true},
		{"canonical and sorted", []string{"x-lang, accept-encoding"}, []string{"Accept-Encoding", "X-Lang"}, true},
		{"duplicates across lines", []string{"Accept-Encoding", "accept-encoding, X-Lang"}, []string{"Accept-Encoding", "X-Lang"}, true},
		{"empty elements", []string{" , ,Accept"}, []string{"Accept"}, true},
		{"star", []string{"*"}, nil, false},
		{"star in a list", []string{"Accept, *"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			names, ok := getVaryNames(http.Header{"Vary": tt.lines})
			if !slices.Equal(names, tt.want) || ok != tt.wantOK {
				t.Errorf("getVaryNames(%q) = %q, %v; want %q, %v", tt.lines, names, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestNormalizeValues checks that equivalent header values normalize to the same string.
func TestNormalizeValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		values []string // header lines
		want   string
	}{
		{"single value", []string{"gzip"}, "gzip"},
		{"spaces around elements", []string{" gzip ,  br "}, "gzip,br"},
		{"several lines", []string{"gzip", "br"}, "gzip,br"},
		{"internal spaces kept", []string{"text/html; q=0.9"}, "text/html; q=0.9"},
		{"order kept", []string{"br, gzip"}, "br,gzip"},
		{"case kept", []string{"GZIP"}, "GZIP"},
		{"empty value", []string{""}, ""},
		{"empty elements dropped", []string{"gzip,, br,", " ,"}, "gzip,br"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeValues(tt.values); got != tt.want {
				t.Errorf("normalizeValues(%q) = %q, want %q", tt.values, got, tt.want)
			}
		})
	}
}
