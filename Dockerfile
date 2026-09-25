# The builder runs natively and cross-compiles, so multi-arch builds need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /cleanarr .

FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /cleanarr /usr/local/bin/cleanarr
ENV CLEANARR_CONFIG=/config CLEANARR_LISTEN=:9797
VOLUME /config
EXPOSE 9797
ENTRYPOINT ["cleanarr"]
