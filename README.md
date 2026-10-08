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
- **OpenTelemetry:** when configured, yarp sends to a collector over OTLP/HTTP:
  - its logs, at the same level as the console;
  - metrics every 15 s: request duration and active requests (`http.server.*`), backend call duration (`http.client.request.duration`), cache hits, misses and bypasses (`yarp.cache.requests`), the time yarp itself adds before sending the response headers (`yarp.request.overhead`) and Go runtime metrics (`go.*`).
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

## Demo

`demo/` is a Docker Compose stack that runs yarp with everything around it. You only need Docker.

```sh
cd demo
docker compose up --build
```

It starts:

- **yarp**, built from the `Dockerfile` and listening on `localhost:8080`. Its config is `demo/yarp.yml` and `demo/routes.yml`, mounted into the container, so you can edit them and run `docker compose restart yarp`.
- **Servers** behind two routes:
  - `/app` goes round-robin to `app-1`, `app-2` and `app-3`;
  - the host `docs.localhost` goes to `docs`.
- **Grafana LGTM** (`grafana/otel-lgtm`) at <http://localhost:3000> (login `admin` / `admin`). yarp sends its metrics, traces and logs there, and the dashboard in `contrib/grafana/yarp.json` is already loaded.
- **A traffic generator** that sends a few requests a second, so the dashboard has data right away.

Things to try:

```sh
# Load balancing: the reply cycles through app-1, app-2, app-3
curl localhost:8080/app

# Host routing
curl -H 'Host: docs.localhost' localhost:8080/

# No matching route: 404
curl -i localhost:8080/nope
```

In Grafana, open **Dashboards → yarp** for traffic, status codes, yarp's own overhead and backends. Use **Explore** with Tempo for traces and Loki for logs.

Stop and remove everything with `docker compose down`. If port 8080 or 3000 is busy, stop what uses it; for Grafana you can instead run `GRAFANA_PORT=3001 docker compose up --build`.

## Grafana dashboard

`contrib/grafana/yarp.json` is a Grafana dashboard for yarp's OpenTelemetry data: traffic, status codes, cache results, yarp's own overhead, backends, Go runtime, traces and logs. It expects the metrics in Prometheus, the traces in Tempo and the logs in Loki, as in the [`grafana/otel-lgtm`](https://github.com/grafana/docker-otel-lgtm) image.

Import it with **Dashboards → New → Import**, then pick the Prometheus, Loki and Tempo data sources at the top of the dashboard.
