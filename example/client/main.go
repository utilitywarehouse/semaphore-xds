package main

import (
	"context"
	"flag"
	"log"
	"strings"
	"sync"
	"time"

	echo "github.com/utilitywarehouse/semaphore-xds/example/client/echo"

	"google.golang.org/grpc"
	_ "google.golang.org/grpc/xds"
)

var (
	flagGrpcServerAddresses = flag.String("grpc-server-addresses", "", "Comma separated list of echo server addresses")
)

func main() {
	flag.Parse()
	if *flagGrpcServerAddresses == "" {
		log.Fatal("Must provide a list grpc server addresses")
	}
	addresses := strings.Split(*flagGrpcServerAddresses, ",")
	var wg sync.WaitGroup
	for _, address := range addresses {
		wg.Add(1)
		go func(address string) {
			log.Println("Looking up service %s", address)
			conn, err := grpc.Dial(address, grpc.WithInsecure())
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
		}(address)
	}
	wg.Wait()
}
