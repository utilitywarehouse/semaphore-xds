package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/scaleway/scaleway-sdk-go/logger"
	echo "github.com/utilitywarehouse/semaphore-xds/example/client-on-demand/echo"

	grpc_logrus "github.com/grpc-ecosystem/go-grpc-middleware/logging/logrus"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/xds"
)

var (
	flagGrpcServerAddress = flag.String("grpc-server-address", "", "Echo server address")
)

var echoServerClient echo.EchoServerClient

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
	echoServerClient = echo.NewEchoServerClient(conn)
	sayHello()

	http.HandleFunc("/", sayHelloHandler)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		logger.Errorf("%v", err)
	}

}

func sayHello() string {
	r, err := echoServerClient.SayHello(context.Background(), &echo.EchoRequest{Name: "unary RPC msg "})
	if err != nil {
		log.Printf("Could not get RPC %v\n", err)
		return fmt.Sprintf("Could not get RPC %v\n", err)
	}
	log.Printf("RPC Response: %v", r)
	return fmt.Sprintf("RPC Response: %v", r)
}

func sayHelloHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, sayHello())
}
