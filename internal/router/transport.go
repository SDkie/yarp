package router

import (
	"net"
	"net/http"
	"time"
)

const (
	// dialTimeout limits how long connecting to a backend may take.
	dialTimeout = 30 * time.Second
	// tcpKeepAlive is the interval between TCP keep-alive probes on
	// backend connections.
	tcpKeepAlive = 30 * time.Second
	// idleConnTimeout is how long an idle backend connection stays in the
	// pool before it is closed.
	idleConnTimeout = 90 * time.Second
	// tlsHandshakeTimeout limits the TLS handshake with https backends.
	tlsHandshakeTimeout = 10 * time.Second
	// expectContinueTimeout is how long to wait for a backend's
	// "100 Continue" before sending a request body anyway.
	expectContinueTimeout = 1 * time.Second
	// maxIdleConnsPerHost is the maximum number of idle connections to each
	// backend server kept open for reuse. Go's default of 2 forces most
	// connections to be closed and reopened under concurrent load.
	maxIdleConnsPerHost = 200
)

// newTransport returns the transport shared by all route proxies, giving
// yarp its own connection pool.
func newTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: tcpKeepAlive,
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
		// MaxIdleConns is left at 0 (no limit): Go's default cap of 100
		// across all backends would otherwise override the per-backend
		// limit. Idle connections are still closed after IdleConnTimeout.
	}
}
