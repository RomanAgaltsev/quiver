// Command local is the hermetic example server for examples/local (Q35): it
// backs the collection's login → me capture chain, its GraphQL query, and its
// gRPC unary call, with no dependency on any third-party service.
//
// The handlers live in ./app so the end-to-end test can run the shipped
// collection against exactly this code.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"time"

	"google.golang.org/grpc"

	"github.com/RomanAgaltsev/quiver/examples/local/server/app"
)

func main() {
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", ":50052")
	if err != nil {
		log.Fatalf("listen grpc: %v", err)
	}
	grpcSrv := grpc.NewServer()
	app.RegisterGRPC(grpcSrv)
	go func() { log.Fatal(grpcSrv.Serve(lis)) }()
	log.Println("grpc  listening on :50052")

	httpSrv := &http.Server{
		Addr:              ":8080",
		Handler:           app.NewHTTPHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Println("http  listening on :8080")
	log.Fatal(httpSrv.ListenAndServe())
}
