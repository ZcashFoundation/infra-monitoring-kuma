# Build
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/prober .

# Runtime — distroless, non-root, outbound-only (no inbound except the HTTP server)
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/prober /app/prober
COPY seeders.yaml /app/seeders.yaml
EXPOSE 8080
# Cloud Run sets $PORT; default to 8080. Re-probe every 15m (nodes rate-limit
# rapid reprobes — see README). /healthz returns 503 on any hard-down.
ENTRYPOINT ["/app/prober"]
CMD ["--serve", ":8080", "--interval", "15m", "--config", "/app/seeders.yaml"]
