package main

import (
	"context"
	"embed"
	_ "embed"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	pingv1 "connectrpc.com/connect/internal/gen/connect/ping/v1"
	"connectrpc.com/connect/internal/gen/connect/ping/v1/pingv1connect"
)

//go:embed wasm_exec.js
//go:embed client.wasm
//go:embed index.html
var content embed.FS

func main() {
	mux := http.NewServeMux()
	mux.Handle(
		pingv1connect.NewPingServiceHandler(
			&ExamplePingServer{}, // our business logic
		),
	)
	mux.Handle("/", http.FileServer(http.FS(content)))

	fmt.Println("Listening on port 8080")
	http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "*")
		mux.ServeHTTP(w, r)
	}))
}

// ExamplePingServer implements some trivial business logic. The Protobuf
// definition for this API is in proto/connect/ping/v1/ping.proto.
type ExamplePingServer struct {
	pingv1connect.UnimplementedPingServiceHandler
}

// Ping implements pingv1connect.PingServiceHandler.
func (*ExamplePingServer) Ping(
	_ context.Context,
	request *connect.Request[pingv1.PingRequest],
) (*connect.Response[pingv1.PingResponse], error) {
	fmt.Println("got ping request", request.Msg.Text)
	return connect.NewResponse(
		&pingv1.PingResponse{
			Number: request.Msg.Number,
			Text:   request.Msg.Text,
		},
	), nil
}
