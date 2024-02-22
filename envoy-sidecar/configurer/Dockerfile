FROM golang:1.21-alpine AS build
WORKDIR /go/src/github.com/utilitywarehouse/semaphore-xds/envoy-sidecar/configurer
COPY . /go/src/github.com/utilitywarehouse/semaphore-xds/envoy-sidecar/configurer
ENV CGO_ENABLED=0
RUN \
  apk --no-cache add git upx \
    && go build -ldflags='-s -w' -o /semaphore-xds-envoy-configurer . \
    && upx /semaphore-xds-envoy-configurer

FROM alpine:3.18
COPY --from=build /semaphore-xds-envoy-configurer /semaphore-xds-envoy-configurer
ENTRYPOINT [ "/semaphore-xds-envoy-configurer" ]
