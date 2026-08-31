// Package app holds the hermetic example server's handlers.
//
// They live in an importable package rather than in main so that the end-to-end
// test can run the shipped example collection against exactly the handlers the
// README tells a reader to start. An example nothing runs is an example that
// rots — the previous revision's /graphql endpoint was dead precisely because
// no request file and no test ever called it.
package app

import (
	"context"
	"encoding/json"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/RomanAgaltsev/quiver/internal/transport/grpcx/echopb"
)

// LoginToken is the bearer credential /login hands out and /me demands. The
// example's capture chain exists to move exactly this value between requests.
const LoginToken = "tok-ada-42"

// NewHTTPHandler returns the example's HTTP and GraphQL endpoints.
func NewHTTPHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": LoginToken})
	})

	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+LoginToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "Ada", "login": "ada"})
	})

	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"hero":{"name":"R2-D2"}}}`))
	})

	return mux
}

type echoService struct{ echopb.UnimplementedEchoServer }

func (echoService) Say(_ context.Context, in *echopb.EchoRequest) (*echopb.EchoReply, error) {
	return &echopb.EchoReply{Msg: "got:" + in.Msg}, nil
}

// RegisterGRPC adds the example's unary echo service, with server reflection
// enabled so the collection needs no .proto file.
func RegisterGRPC(srv *grpc.Server) {
	echopb.RegisterEchoServer(srv, echoService{})
	reflection.Register(srv)
}
