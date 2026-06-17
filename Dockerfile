# --- build ---
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
# Resolve dependencies and generate go.sum inside the build (no local Go needed).
# Commit the resulting go.sum and you can drop this line for reproducible builds.
RUN go mod tidy
# Static binary; web/ is embedded via go:embed so nothing else ships.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /dev-env-config .

# --- run ---
# distroless: no shell, no package manager, runs as nonroot (uid 65532).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /dev-env-config /dev-env-config
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/dev-env-config"]
