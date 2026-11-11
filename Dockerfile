FROM --platform=$BUILDPLATFORM golang:1.26@sha256:5ebd6e7dc13f9e273ed9470679eb295d8b6e7de3b6379c8a5e17a9e6c3eabd38 AS builder
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
    -X github.com/samouraiworld/yagura/internal/version.Version=$VERSION \
    -X github.com/samouraiworld/yagura/internal/version.Revision=$REVISION" \
    -o /out/yagura ./cmd/yagura

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/samouraiworld/yagura" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="$VERSION"
COPY --from=builder /out/yagura /yagura
USER 65532:65532
EXPOSE 10100
ENTRYPOINT ["/yagura"]
