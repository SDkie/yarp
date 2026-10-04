# yarp

yarp (Yet Another Reverse Proxy) is a small HTTP reverse proxy written in Go. It listens on one or more addresses, matches each request to a route by host and path, and forwards it to a backend server. Responses can be cached on disk, following the HTTP caching standard (RFC 9111).

## Features

- **Entry points:** listen on several addresses at once.
- **Routing:** by host (exact, or a `*.example.com` wildcard) and path prefix. The most specific route wins.
- **Load balancing:** round-robin across a route's servers (HTTP or HTTPS).
- **Proxy headers:** sets `X-Forwarded-For`, `X-Forwarded-Host` and `X-Forwarded-Proto`, and removes hop-by-hop headers.
- **HTTP cache** (on by default, stored in `.cache/`):
  - caches `GET` and `HEAD` responses with `s-maxage`, `max-age` or `Expires`, and serves them only while fresh;
  - supports `Vary` and sends an `Age` header on cached responses;
  - answers clients' `If-None-Match` / `If-Modified-Since` with `304 Not Modified`;
  - never stores `no-store`, `private`, `no-cache` or `Set-Cookie` responses;
  - clears a URL's cached responses after a successful `POST`, `PUT`, `DELETE` or other unsafe request;
  - adds a `Cache-Status` header (`hit`, `fwd=miss` or `fwd=bypass`) to every response.
- **OpenTelemetry:** set up to send to a collector over OTLP/HTTP when configured. Traces, metrics and logs are added next.
- **Logging:** structured logs on stderr, with a configurable level (`ERROR` by default).
- **Strict config:** unknown fields and invalid values are rejected at startup.
- **Graceful shutdown** on `SIGINT` / `SIGTERM`.

## Usage

Build:

```sh
go build -o yarp ./cmd/yarp
```

Create `yarp.yml`:

```yaml
entryPoints:
  web:
    address: :8080

providers:
  file:
    filename: routes.yml

cache:
  enabled: true # optional; defaults to true

log:
  level: INFO # optional; DEBUG, INFO, WARN or ERROR; defaults to ERROR

otel: # optional; turns on OpenTelemetry
  endpoint: http://localhost:4318 # OTLP/HTTP collector
```

Create `routes.yml`:

```yaml
routes:
  api:
    host: example.com   # optional
    pathPrefix: /api    # optional, but host or pathPrefix is required
    entryPoints:
      - web
    servers:
      - url: http://127.0.0.1:5678
      - url: http://127.0.0.1:5679
```

Run:

```sh
./yarp -config yarp.yml
```

A request to `http://example.com:8080/api/users` is forwarded to one of the two servers. Requests that match no route get `404`.
