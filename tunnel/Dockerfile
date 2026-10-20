# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags "-s -w \
        -X github.com/nogataka/gridbase-oss/tunnel/pkg/buildinfo.Version=${VERSION} \
        -X github.com/nogataka/gridbase-oss/tunnel/pkg/buildinfo.CommitSHA=${COMMIT} \
        -X github.com/nogataka/gridbase-oss/tunnel/pkg/buildinfo.BuildTime=${BUILD_TIME}" \
      -o /out/gridbase-tunnel-server ./server \
 && go build -trimpath -ldflags "-s -w" -o /out/gridbase-tunnel-client ./client

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gridbase-tunnel-server /usr/local/bin/gridbase-tunnel-server
COPY --from=build /out/gridbase-tunnel-client /usr/local/bin/gridbase-tunnel-client
COPY config/server.example.json /etc/gridbase/server.example.json

# 9080: HTTP proxy + admin API (behind Caddy), 9443: tunnel port (clients connect here)
EXPOSE 9080 9443
HEALTHCHECK --interval=10s --timeout=4s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/gridbase-tunnel-server", "--healthcheck", "--config", "/etc/gridbase/server.json"]

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/gridbase-tunnel-server"]
CMD ["--config", "/etc/gridbase/server.json"]
