FROM golang:1.19-alpine AS build
WORKDIR /go/src/github.com/utilitywarehouse/semaphore-xDS
COPY . /go/src/github.com/utilitywarehouse/semaphore-xDS
ENV CGO_ENABLED=0
RUN \
  apk --no-cache add git upx \
    && go get -t ./... \
    && go test -v \
    && go build -ldflags='-s -w' -o /semaphore-xDS . \
    && upx /semaphore-xDS \
    && cd example/server/ \
    && go build -ldflags='-s -w' -o /semaphore-xDS-echo-server . \
    && cd ../client/ \
    && go build -ldflags='-s -w' -o /semaphore-xDS-echo-client .

FROM alpine:3.15
COPY --from=build /semaphore-xDS /semaphore-xDS
COPY --from=build /semaphore-xDS-echo-server /semaphore-xDS-echo-server
COPY --from=build /semaphore-xDS-echo-client /semaphore-xDS-echo-client
ENTRYPOINT [ "/semaphore-xDS" ]
