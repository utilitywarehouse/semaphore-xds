package main

import (
	"context"
	"log"
	"time"

	echo "github.com/utilitywarehouse/semaphore-xds/example/client/echo"

	"google.golang.org/grpc"
	_ "google.golang.org/grpc/xds"
)

func main() {
	serverAddr1 := "xds:///grpc-echo-server.labs:50051"
	serverAddr2 := "xds:///grpc-echo-another-server.labs:50051"
	conn1, err := grpc.Dial(serverAddr1, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("Could not connect %v", err)
	}
	defer conn1.Close()
	c1 := echo.NewEchoServerClient(conn1)
	ctx := context.Background()
	r1, err := c1.SayHello(ctx, &echo.EchoRequest{Name: "unary RPC msg "})
	if err != nil {
		log.Printf("Could not get RPC %v\n", err)
	}
	log.Printf("RPC Response: %v", r1)
	time.Sleep(1 * time.Second)
	conn2, err := grpc.Dial(serverAddr2, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("Could not connect %v", err)
	}
	defer conn1.Close()
	c2 := echo.NewEchoServerClient(conn2)
	r2, err := c2.SayHello(ctx, &echo.EchoRequest{Name: "unary RPC msg "})
	if err != nil {
		log.Printf("Could not get RPC %v\n", err)
	}
	log.Printf("RPC Response: %v", r2)
}
