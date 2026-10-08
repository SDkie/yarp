package telemetry

import (
	"net/http"
	"reflect"
	"testing"
)

// TestSetCacheResult checks that a reported cache result is added to the server span and counted, and a missing one is not.
func TestSetCacheResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		result     string // "" reports nothing
		wantAttr   string // the span's yarp.cache.result; "" means absent
		wantPoints []point
	}{
		{"hit", "hit", "hit", []point{{map[string]string{"yarp.cache.result": "hit"}, 1}}},
		{"bypass", "bypass", "bypass", []point{{map[string]string{"yarp.cache.result": "bypass"}, 1}}},
		{"not reported", "", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tel := newTestTelemetry(t)
			serve(tel, func(w http.ResponseWriter, r *http.Request) {
				if tt.result != "" {
					SetCacheResult(r.Context(), tt.result)
				}
			}, http.MethodGet, "http://example.com/")

			got, ok := attrMap(tel.span(t).Attributes())["yarp.cache.result"]
			if got != tt.wantAttr || ok != (tt.wantAttr != "") {
				t.Errorf("span yarp.cache.result = %q (set %v), want %q", got, ok, tt.wantAttr)
			}
			if points := tel.points(t, "yarp.cache.requests"); !reflect.DeepEqual(points, tt.wantPoints) {
				t.Errorf("yarp.cache.requests = %v, want %v", points, tt.wantPoints)
			}
		})
	}
}

// TestMarkCacheEnabled checks that yarp.cache.enabled is 0 until the cache is marked enabled, then 1.
func TestMarkCacheEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mark bool
		want int64
	}{
		{"not marked", false, 0},
		{"marked", true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tel := newTestTelemetry(t)
			if tt.mark {
				tel.MarkCacheEnabled()
			}
			want := []point{{map[string]string{}, tt.want}}
			if got := tel.points(t, "yarp.cache.enabled"); !reflect.DeepEqual(got, want) {
				t.Errorf("yarp.cache.enabled = %v, want %v", got, want)
			}
		})
	}
}

// TestMarkCacheEnabledOff checks that marking a nil Telemetry is safe.
func TestMarkCacheEnabledOff(t *testing.T) {
	t.Parallel()
	var tel *Telemetry
	tel.MarkCacheEnabled()
}
