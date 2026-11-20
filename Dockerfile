# syntax=docker/dockerfile:1.7

FROM golang:1.27.4-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/yugrinovD/gogate/internal/version.Version=${VERSION} -X github.com/yugrinovD/gogate/internal/version.Commit=${COMMIT} -X github.com/yugrinovD/gogate/internal/version.Date=${DATE}" \
      -o /out/gogate ./cmd/gogate && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/gogate-mockupstream ./cmd/gogate-mockupstream

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/gogate /app/gogate
COPY --from=build /out/gogate-mockupstream /app/gogate-mockupstream
USER nonroot:nonroot
EXPOSE 8080 9090
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=6 \
  CMD ["/app/gogate", "healthcheck", "--url", "http://127.0.0.1:8080/readyz"]
ENTRYPOINT ["/app/gogate"]
