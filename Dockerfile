# --- build ---
FROM golang:1.22-alpine AS build
WORKDIR /src
# Download deps first (cached layer) — only re-runs when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binary; web/ is embedded via go:embed so nothing else ships.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /dev-env-config .

# --- run ---
# distroless: no shell, no package manager, runs as nonroot (uid 65532).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /dev-env-config /dev-env-config
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/dev-env-config"]
