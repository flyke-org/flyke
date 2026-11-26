# Dev image for the control plane. Source is bind-mounted at /src at runtime;
# air rebuilds and restarts flyke-server when Go files change.
# Alpine is enough: the SQLite driver (modernc.org/sqlite) is pure Go, no cgo.
FROM golang:1.26-alpine
RUN go install github.com/air-verse/air@latest \
 && go clean -cache -modcache
WORKDIR /src
EXPOSE 8742
