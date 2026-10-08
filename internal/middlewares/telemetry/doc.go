// Package telemetry sends traces, metrics and logs to an OpenTelemetry
// collector over OTLP/HTTP. Setup starts it; a nil *Telemetry means it is
// off.
//
// Spans, named after the request method:
//   - server: each request received on an entry point (Handler)
//   - client: each request sent to a backend (Transport)
//
// Metrics:
//   - http.server.request.duration, http.server.active_requests (Handler)
//   - http.client.request.duration (Transport)
//   - yarp.cache.requests (SetCacheResult)
//   - yarp.cache.enabled (MarkCacheEnabled)
//   - Go runtime metrics
//
// Logs: the default slog logger's records at or above the given level
// (Setup).
//
// yarp's own attributes:
//   - yarp.entrypoint: server span and server metrics
//   - yarp.route: both spans, server and client duration
//   - yarp.cache.result: server span and yarp.cache.requests
package telemetry
