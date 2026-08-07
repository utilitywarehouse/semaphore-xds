FROM golang:1.26-alpine AS build
WORKDIR /go/src/github.com/utilitywarehouse/semaphore-xds
COPY . /go/src/github.com/utilitywarehouse/semaphore-xds
ENV CGO_ENABLED=0
# GOTOOLCHAIN pins the exact toolchain declared by go.mod's `go` line, so the
# build isn't at the mercy of whatever patch version the base image ships.
RUN \
  apk --no-cache add git \
    && GOTOOLCHAIN=go$(awk '/^go /{print $2; exit}' go.mod) \
    && go mod download \
    && go test -v ./... \
    && go build -ldflags='-s -w' -o /semaphore-xds . \
    && cd example/server/ \
    && go build -ldflags='-s -w' -o /semaphore-xds-echo-server . \
    && cd ../client/ \
    && go build -ldflags='-s -w' -o /semaphore-xds-echo-client .

FROM alpine:3.24
COPY --from=build /semaphore-xds /semaphore-xds
COPY --from=build /semaphore-xds-echo-server /semaphore-xds-echo-server
COPY --from=build /semaphore-xds-echo-client /semaphore-xds-echo-client
ENTRYPOINT [ "/semaphore-xds" ]
