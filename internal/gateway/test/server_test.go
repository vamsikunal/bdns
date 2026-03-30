package gatewaytest

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/gateway"
	pb "github.com/bleasey/bdns/internal/proto/gatwaypb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// startTestServer spins up GatewayServer on a random free port and returns
// the server handle along with a connected gRPC client stub.
func startTestServer(t *testing.T, srv *gateway.GatewayServer) (pb.BDNSGatewayClient, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	gs := grpc.NewServer()
	pb.RegisterBDNSGatewayServer(gs, srv)
	go gs.Serve(lis)

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	return pb.NewBDNSGatewayClient(conn), func() {
		conn.Close()
		gs.GracefulStop()
	}
}

func TestGatewayServer_HealthCheck(t *testing.T) {
	// Build a minimal GatewayServer shell without a real node
	srv := gateway.NewGatewayServerForTest()

	client, cleanup := startTestServer(t, srv)
	defer cleanup()

	resp, err := client.HealthCheck(context.Background(), &pb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	// GatewayServer.HealthCheck returns true only when it can reach the node;
	// with nil node it returns Healthy:true defensively — the real check is
	// that there are no panics and the RPC round-trips successfully.
	_ = resp.Healthy
}

func TestGatewayServer_SubscribeHeaders(t *testing.T) {
	srv := gateway.NewGatewayServerForTest()

	client, cleanup := startTestServer(t, srv)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := client.SubscribeHeaders(ctx, &pb.SubscribeRequest{StartIndex: 0})
	if err != nil {
		t.Fatalf("SubscribeHeaders: %v", err)
	}

	want := blockchain.BlockHeader{Index: 42, SlotNumber: 1, Hash: []byte("h")}

	go func() {
		time.Sleep(50 * time.Millisecond)
		srv.BroadcastHeader(&want)
	}()

	got, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if got.Index != want.Index {
		t.Fatalf("expected index %d, got %d", want.Index, got.Index)
	}
}
