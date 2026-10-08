FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/yarp ./cmd/yarp

FROM scratch
# CA certificates, for HTTPS servers.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/yarp /yarp
# The cache is written to .cache in the working directory.
WORKDIR /var/lib/yarp
ENTRYPOINT ["/yarp"]
CMD ["-config", "/etc/yarp/yarp.yml"]
