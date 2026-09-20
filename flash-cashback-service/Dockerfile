# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src

# Cache module downloads. go.sum is generated here on first build (GOFLAGS=-mod=mod).
ENV GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux
COPY go.mod ./
COPY go.sum* ./
RUN go mod download || true

COPY . .
# Resolve the full dependency graph and build a static binary.
RUN go mod tidy && go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
