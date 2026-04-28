package grpcreflect

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func TestListServices(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	reflection.Register(server)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := ListServices(ctx, Params{Target: listener.Addr().String(), Plaintext: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Services) == 0 {
		t.Fatalf("expected reflection services: %#v", result)
	}
}
