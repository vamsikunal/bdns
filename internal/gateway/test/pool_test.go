package gatewaytest

import (
	"testing"

	pb "github.com/bleasey/bdns/internal/proto/gatwaypb"
	"github.com/bleasey/bdns/internal/gateway"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockClient is a test double for poolClient
type mockClient struct {
	queryErr  error
	queryResp *pb.DomainQueryResponse
	healthErr error
}

func (m *mockClient) QueryDomain(_ string, _ int64) (*pb.DomainQueryResponse, error) {
	return m.queryResp, m.queryErr
}

func (m *mockClient) HealthCheck() (*pb.HealthCheckResponse, error) {
	return &pb.HealthCheckResponse{Healthy: m.healthErr == nil}, m.healthErr
}

func (m *mockClient) Close() {}

func newTestPool(clients []gateway.PoolClient) *gateway.ConnectionPool {
	addrs := make([]string, len(clients))
	for i := range addrs {
		addrs[i] = "mock"
	}
	p := gateway.NewConnectionPoolForTest(clients, addrs)
	return p
}

func TestConnectionPool_Failover(t *testing.T) {
	ok := &mockClient{queryResp: &pb.DomainQueryResponse{IpAddress: "1.2.3.4"}}
	dead := &mockClient{queryErr: status.Error(codes.Unavailable, "down")}

	p := newTestPool([]gateway.PoolClient{dead, ok})
	p.SetHealth(0, false) // client 0 pre-marked unhealthy

	resp, err := p.QueryWithFailover("test.bdns", 0)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if resp.IpAddress != "1.2.3.4" {
		t.Fatalf("expected 1.2.3.4, got %s", resp.IpAddress)
	}
}

func TestConnectionPool_NXDOMAINNoHealthPenalty(t *testing.T) {
	// A healthy node returning NotFound must not lose its health status
	nxClient := &mockClient{queryErr: status.Error(codes.NotFound, "domain not found")}
	p := newTestPool([]gateway.PoolClient{nxClient})

	_, err := p.QueryWithFailover("ghost.bdns", 0)
	if err == nil {
		t.Fatal("expected NotFound error")
	}
	if !p.GetHealth(0) {
		t.Fatal("healthy node penalised for legitimate NXDOMAIN response")
	}
}

func TestConnectionPool_AllPeersDown(t *testing.T) {
	dead := &mockClient{queryErr: status.Error(codes.Unavailable, "down")}
	p := newTestPool([]gateway.PoolClient{dead, dead})

	_, err := p.QueryWithFailover("any.bdns", 0)
	if err != gateway.ErrAllPeersDown {
		t.Fatalf("expected ErrAllPeersDown, got %v", err)
	}
}

func TestConnectionPool_GetHealthyCount(t *testing.T) {
	ok := &mockClient{}
	p := newTestPool([]gateway.PoolClient{ok, ok, ok})
	p.SetHealth(1, false)

	if n := p.GetHealthyCount(); n != 2 {
		t.Fatalf("expected 2, got %d", n)
	}
}
