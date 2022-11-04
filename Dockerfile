FROM golang:1.19-alpine AS build
WORKDIR /go/src/github.com/utilitywarehouse/semaphore-xds
COPY . /go/src/github.com/utilitywarehouse/semaphore-xds
ENV CGO_ENABLED=0
RUN \
  apk --no-cache add git upx \
    && go get -t ./... \
    && go test -v ./... \
    && go build -ldflags='-s -w' -o /semaphore-xds . \
    && upx /semaphore-xds \
    && cd example/server/ \
    && go build -ldflags='-s -w' -o /semaphore-xds-echo-server . \
    && cd ../client/ \
    && go build -ldflags='-s -w' -o /semaphore-xds-echo-client .

FROM alpine:3.15
COPY --from=build /semaphore-xds /semaphore-xds
COPY --from=build /semaphore-xds-echo-server /semaphore-xds-echo-server
COPY --from=build /semaphore-xds-echo-client /semaphore-xds-echo-client
ENTRYPOINT [ "/semaphore-xds" ]
