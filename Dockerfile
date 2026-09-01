FROM --platform=$BUILDPLATFORM golang:1.23-alpine@sha256:383395b794dffa5b53012a212365d40c8e37109a626ca30d6151c8348d380b5f AS build

ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org

WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY="$GOPROXY" GOSUMDB="$GOSUMDB" go mod download
COPY *.go ./
RUN GOPROXY="$GOPROXY" GOSUMDB="$GOSUMDB" \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/qbit-redownloader .

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/qbit-redownloader /qbit-redownloader

USER 65532:65532
ENTRYPOINT ["/qbit-redownloader"]
