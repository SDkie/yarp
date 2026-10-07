package telemetry

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// serve sends a request for method and target through tel's handler for the
// entry point web, with next as the inner handler.
func serve(tel *testTelemetry, next http.HandlerFunc, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	tel.Handler("web", next).ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// TestHandlerSpan checks the server span's name, kind, attributes and status for each kind of response.
func TestHandlerSpan(t *testing.T) {
	t.Parallel()
	// Attributes every server span has; rows add the rest.
	base := map[string]string{
		"url.scheme":      "http",
		"server.address":  "example.com",
		"url.path":        "/api/x",
		"yarp.entrypoint": "web",
	}
	tests := []struct {
		name       string
		method     string
		next       http.HandlerFunc
		wantName   string
		wantAttrs  map[string]string // added to base
		wantStatus codes.Code
	}{
		{
			name:   "ok",
			method: http.MethodGet,
			next: func(w http.ResponseWriter, r *http.Request) {
				SetRoute(r.Context(), "api")
				io.WriteString(w, "hi")
			},
			wantName:  "GET",
			wantAttrs: map[string]string{"http.request.method": "GET", "yarp.route": "api", "http.response.status_code": "200"},
		},
		{
			name:   "client error is not a span error",
			method: http.MethodGet,
			next: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantName:  "GET",
			wantAttrs: map[string]string{"http.request.method": "GET", "yarp.route": "", "http.response.status_code": "404"},
		},
		{
			name:   "server error",
			method: http.MethodPost,
			next: func(w http.ResponseWriter, r *http.Request) {
				SetRoute(r.Context(), "api")
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantName:   "POST",
			wantAttrs:  map[string]string{"http.request.method": "POST", "yarp.route": "api", "http.response.status_code": "503", "error.type": "503"},
			wantStatus: codes.Error,
		},
		{
			name:      "nothing sent",
			method:    http.MethodGet,
			next:      func(w http.ResponseWriter, r *http.Request) {},
			wantName:  "GET",
			wantAttrs: map[string]string{"http.request.method": "GET", "yarp.route": ""},
		},
		{
			name:   "non-standard method",
			method: "FOO",
			next: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			wantName:  "HTTP",
			wantAttrs: map[string]string{"http.request.method": "_OTHER", "yarp.route": "", "http.response.status_code": "200"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tel := newTestTelemetry(t)
			serve(tel, tt.next, tt.method, "http://example.com/api/x?q=1")

			span := tel.span(t)
			if span.Name() != tt.wantName {
				t.Errorf("span name = %q, want %q", span.Name(), tt.wantName)
			}
			if span.SpanKind() != trace.SpanKindServer {
				t.Errorf("span kind = %v, want server", span.SpanKind())
			}
			want := maps.Clone(base)
			maps.Copy(want, tt.wantAttrs)
			if got := attrMap(span.Attributes()); !maps.Equal(got, want) {
				t.Errorf("span attributes = %v, want %v", got, want)
			}
			if span.Status().Code != tt.wantStatus {
				t.Errorf("span status = %v, want %v", span.Status().Code, tt.wantStatus)
			}
		})
	}
}

// TestHandlerContinuesTrace checks that the server span joins the client's trace and is in next's context.
func TestHandlerContinuesTrace(t *testing.T) {
	t.Parallel()
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	tel := newTestTelemetry(t)
	var inNext trace.SpanContext
	next := func(w http.ResponseWriter, r *http.Request) {
		inNext = trace.SpanContextFromContext(r.Context())
	}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	r.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	tel.Handler("web", http.HandlerFunc(next)).ServeHTTP(httptest.NewRecorder(), r)

	span := tel.span(t)
	if got := span.SpanContext().TraceID().String(); got != traceID {
		t.Errorf("trace ID = %s, want %s", got, traceID)
	}
	if got := span.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("parent span ID = %s, want 00f067aa0ba902b7", got)
	}
	if inNext.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("next's span = %s, want the server span %s", inNext.SpanID(), span.SpanContext().SpanID())
	}
}

// TestHandlerMetrics checks the request duration and active requests metrics, during and after a request.
func TestHandlerMetrics(t *testing.T) {
	t.Parallel()
	tel := newTestTelemetry(t)
	activeAttrs := map[string]string{"http.request.method": "GET", "url.scheme": "http", "yarp.entrypoint": "web"}
	var during []point
	next := func(w http.ResponseWriter, r *http.Request) {
		SetRoute(r.Context(), "api")
		during = tel.points(t, "http.server.active_requests")
		w.WriteHeader(http.StatusCreated)
	}
	serve(tel, next, http.MethodGet, "http://example.com/api")

	if want := []point{{activeAttrs, 1}}; !reflect.DeepEqual(during, want) {
		t.Errorf("active requests during the request = %v, want %v", during, want)
	}
	if got, want := tel.points(t, "http.server.active_requests"), []point{{activeAttrs, 0}}; !reflect.DeepEqual(got, want) {
		t.Errorf("active requests after = %v, want %v", got, want)
	}
	durationAttrs := maps.Clone(activeAttrs)
	durationAttrs["yarp.route"] = "api"
	durationAttrs["http.response.status_code"] = "201"
	if got, want := tel.points(t, "http.server.request.duration"), []point{{durationAttrs, 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("request duration = %v, want %v", got, want)
	}
}

// TestHandlerPanic checks that a request whose handler panics still ends its span, stops being active and records a duration.
func TestHandlerPanic(t *testing.T) {
	t.Parallel()
	tel := newTestTelemetry(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not reach the caller")
			}
		}()
		serve(tel, func(w http.ResponseWriter, r *http.Request) { panic("boom") }, http.MethodGet, "http://example.com/")
	}()

	tel.span(t)
	points := tel.points(t, "http.server.active_requests")
	if len(points) != 1 || points[0].value != 0 {
		t.Errorf("active requests = %v, want 0", points)
	}
	// Nothing was sent, so the duration has no status code.
	want := []point{{map[string]string{"http.request.method": "GET", "url.scheme": "http", "yarp.entrypoint": "web", "yarp.route": ""}, 1}}
	if got := tel.points(t, "http.server.request.duration"); !reflect.DeepEqual(got, want) {
		t.Errorf("request duration = %v, want %v", got, want)
	}
}

// TestResponseWriter checks that the first final status is recorded, 1xx are skipped and a bare Write means 200.
func TestResponseWriter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		write func(w http.ResponseWriter)
		want  int
	}{
		{"nothing sent", func(w http.ResponseWriter) {}, 0},
		{"WriteHeader", func(w http.ResponseWriter) { w.WriteHeader(http.StatusAccepted) }, http.StatusAccepted},
		{"Write only", func(w http.ResponseWriter) { io.WriteString(w, "x") }, http.StatusOK},
		{"1xx then final", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusEarlyHints)
			w.WriteHeader(http.StatusNoContent)
		}, http.StatusNoContent},
		{"first final wins", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadGateway)
			w.WriteHeader(http.StatusOK)
		}, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rw := &responseWriter{ResponseWriter: httptest.NewRecorder()}
			tt.write(rw)
			if rw.status != tt.want {
				t.Errorf("status = %d, want %d", rw.status, tt.want)
			}
		})
	}
}

// TestResponseWriterUnwrap checks that Unwrap returns the wrapped writer, for http.ResponseController.
func TestResponseWriterUnwrap(t *testing.T) {
	t.Parallel()
	inner := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: inner}
	if rw.Unwrap() != inner {
		t.Error("Unwrap did not return the wrapped writer")
	}
	if err := http.NewResponseController(rw).Flush(); err != nil {
		t.Errorf("Flush through the controller: %v", err)
	}
	if !inner.Flushed {
		t.Error("the wrapped writer was not flushed")
	}
}
