FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
    -ldflags "-s -w \
    -X github.com/samourai-crew/yagura/internal/version.Version=$VERSION \
    -X github.com/samourai-crew/yagura/internal/version.Revision=$REVISION" \
    -o /out/yagura ./cmd/yagura

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/samourai-crew/yagura" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="$VERSION"
COPY --from=builder /out/yagura /yagura
USER 65532:65532
EXPOSE 10100
ENTRYPOINT ["/yagura"]
