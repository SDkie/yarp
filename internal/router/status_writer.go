package router

import "net/http"

// statusWriter records the status code sent to the client. It stays 0 when
// nothing was sent, such as when the client went away.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	// 1xx responses are interim; only the first final status counts.
	if sw.status == 0 && code >= 200 {
		sw.status = code
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	return sw.ResponseWriter.Write(b)
}

// Unwrap gives http.ResponseController access to the underlying writer, so
// flushing and hijacking keep working.
func (sw *statusWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}
