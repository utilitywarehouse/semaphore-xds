package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	echo "github.com/utilitywarehouse/semaphore-xds/example/client/echo"

	grpc_logrus "github.com/grpc-ecosystem/go-grpc-middleware/logging/logrus"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/xds"
)

var (
	flagGrpcServerAddress = flag.String("grpc-server-address", "", "Echo server address")
)

func main() {
	flag.Parse()
	if *flagGrpcServerAddress == "" {
		log.Fatal("Must provide a grpc server address")
	}
	log.Println("Looking up service %s", *flagGrpcServerAddress)

	address := fmt.Sprintf("xds:///" + *flagGrpcServerAddress)
	grpc_logrus.ReplaceGrpcLogger(logrus.NewEntry(logrus.StandardLogger()))
	grpcDialOpts := []grpc.DialOption{
		grpc.WithInsecure(),
		grpc.WithUnaryInterceptor(grpc_logrus.UnaryClientInterceptor(logrus.NewEntry(logrus.StandardLogger()))),
	}
	conn, err := grpc.Dial(address, grpcDialOpts...)
	if err != nil {
		log.Fatalf("Could not connect %v", err)
	}
	defer conn.Close()

	c := echo.NewEchoServerClient(conn)
	ctx := context.Background()
	for {
		r, err := c.SayHello(ctx, &echo.EchoRequest{Name: "unary RPC msg "})
		if err != nil {
			log.Printf("Could not get RPC %v\n", err)
		}
		log.Printf("RPC Response: %v", r)
		time.Sleep(1 * time.Second)
	}
}
