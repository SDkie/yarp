package telemetry

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// roundTripFunc lets a function stand in for the backend transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// respond returns a transport that answers every request with status, or
// fails with err when it is set.
func respond(status int, err error) roundTripFunc {
	return func(*http.Request) (*http.Response, error) {
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Body: http.NoBody}, nil
	}
}

// send sends a GET for target, with ctx as its context, through tel's
// transport for the route api.
func send(t *testing.T, ctx context.Context, tel *testTelemetry, next http.RoundTripper, target string) {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if resp, err := tel.Transport("api", next).RoundTrip(r); err == nil {
		resp.Body.Close()
	}
}

var errRefused = errors.New("connection refused")

// TestTransportSpan checks the client span's name, kind, attributes and status for each kind of backend answer.
func TestTransportSpan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		target      string
		next        roundTripFunc
		wantAttrs   map[string]string // added to the method and route
		wantStatus  codes.Code
		wantMessage string
	}{
		{
			name:   "ok",
			target: "http://user:pw@backend:9000/x?token=secret#f",
			next:   respond(http.StatusOK, nil),
			wantAttrs: map[string]string{
				"server.address": "backend", "server.port": "9000", "url.full": "http://backend:9000/x",
				"http.response.status_code": "200",
			},
		},
		{
			name:   "default http port",
			target: "http://backend/x",
			next:   respond(http.StatusNoContent, nil),
			wantAttrs: map[string]string{
				"server.address": "backend", "server.port": "80", "url.full": "http://backend/x",
				"http.response.status_code": "204",
			},
		},
		{
			name:   "default https port",
			target: "https://backend/x",
			next:   respond(http.StatusOK, nil),
			wantAttrs: map[string]string{
				"server.address": "backend", "server.port": "443", "url.full": "https://backend/x",
				"http.response.status_code": "200",
			},
		},
		{
			name:   "client error is a span error",
			target: "http://backend/x",
			next:   respond(http.StatusNotFound, nil),
			wantAttrs: map[string]string{
				"server.address": "backend", "server.port": "80", "url.full": "http://backend/x",
				"http.response.status_code": "404", "error.type": "404",
			},
			wantStatus: codes.Error,
		},
		{
			name:   "no response",
			target: "http://backend/x",
			next:   respond(0, errRefused),
			wantAttrs: map[string]string{
				"server.address": "backend", "server.port": "80", "url.full": "http://backend/x",
				"error.type": "*errors.errorString",
			},
			wantStatus:  codes.Error,
			wantMessage: "connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tel := newTestTelemetry(t)
			send(t, context.Background(), tel, tt.next, tt.target)

			span := tel.span(t)
			if span.Name() != "GET" || span.SpanKind() != trace.SpanKindClient {
				t.Errorf("span = %q %v, want GET client", span.Name(), span.SpanKind())
			}
			want := map[string]string{"http.request.method": "GET", "yarp.route": "api"}
			maps.Copy(want, tt.wantAttrs)
			if got := attrMap(span.Attributes()); !maps.Equal(got, want) {
				t.Errorf("span attributes = %v, want %v", got, want)
			}
			if got := span.Status(); got.Code != tt.wantStatus || got.Description != tt.wantMessage {
				t.Errorf("span status = %v %q, want %v %q", got.Code, got.Description, tt.wantStatus, tt.wantMessage)
			}
		})
	}
}

// TestTransportInjectsTraceparent checks that the backend request carries the client span's trace context.
func TestTransportInjectsTraceparent(t *testing.T) {
	t.Parallel()
	tel := newTestTelemetry(t)
	var traceparent string
	next := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		traceparent = r.Header.Get("traceparent")
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	send(t, context.Background(), tel, next, "http://backend/x")

	sc := tel.span(t).SpanContext()
	if want := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"; traceparent != want {
		t.Errorf("traceparent = %q, want %q", traceparent, want)
	}
}

// TestTransportMetrics checks the client request duration, by status or by error, and that requests the client cancelled are not counted.
func TestTransportMetrics(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		"http.request.method": "GET", "server.address": "backend", "server.port": "80", "yarp.route": "api",
	}
	tests := []struct {
		name         string
		cancelClient bool
		next         roundTripFunc
		wantAttrs    map[string]string // added to base; nil means not counted
	}{
		{"status", false, respond(http.StatusBadGateway, nil), map[string]string{"http.response.status_code": "502"}},
		{"error", false, respond(0, errRefused), map[string]string{"error.type": "*errors.errorString"}},
		{"cancelled by the backend side", false, respond(0, context.Canceled), map[string]string{"error.type": "*errors.errorString"}},
		{"cancelled by the client", true, respond(0, context.Canceled), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tel := newTestTelemetry(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelClient {
				cancel()
			}
			send(t, ctx, tel, tt.next, "http://backend/x")

			tel.span(t) // ended even when not counted
			var want []point
			if tt.wantAttrs != nil {
				attrs := maps.Clone(base)
				maps.Copy(attrs, tt.wantAttrs)
				want = []point{{attrs, 1}}
			}
			if got := tel.points(t, "http.client.request.duration"); !reflect.DeepEqual(got, want) {
				t.Errorf("request duration = %v, want %v", got, want)
			}
		})
	}
}
