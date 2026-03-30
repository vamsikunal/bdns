package gateway

import (
	"errors"
	"log"
	"sync"
	"time"

	pb "github.com/bleasey/bdns/internal/proto/gatwaypb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrAllPeersDown = errors.New("all full node peers unavailable")

// PoolClient is the subset of GatewayClient that the pool depends on
type PoolClient interface {
	QueryDomain(domain string, blockIndex int64) (*pb.DomainQueryResponse, error)
	HealthCheck() (*pb.HealthCheckResponse, error)
	Close()
}

// ConnectionPool manages connections to multiple full nodes with automatic failover
type ConnectionPool struct {
	mu      sync.RWMutex
	clients []PoolClient
	addrs   []string
	health  map[int]bool
	current int
}

// NewConnectionPool initialises a pool from pre-constructed clients.
func NewConnectionPool(clients []PoolClient, addrs []string) *ConnectionPool {
	p := &ConnectionPool{
		clients: clients,
		addrs:   addrs,
		health:  make(map[int]bool),
	}
	for i := range clients {
		p.health[i] = true
	}
	go p.monitorHealth()
	return p
}

// QueryWithFailover resolves a domain against the first healthy full node.
func (p *ConnectionPool) QueryWithFailover(domain string, blockIndex int64) (*pb.DomainQueryResponse, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	start := p.current
	for i := 0; i < len(p.clients); i++ {
		idx := (start + i) % len(p.clients)
		if !p.health[idx] {
			continue
		}

		resp, err := p.clients[idx].QueryDomain(domain, blockIndex)
		if err == nil {
			p.current = idx
			return resp, nil
		}

		if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
			return nil, err
		}

		log.Printf("[POOL] query failed on %s: %v", p.addrs[idx], err)
		p.health[idx] = false
	}

	return nil, ErrAllPeersDown
}

// Clients returns a snapshot of the underlying pool clients
func (p *ConnectionPool) Clients() []PoolClient {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]PoolClient, len(p.clients))
	copy(out, p.clients)
	return out
}

// GetHealthyCount returns the number of currently reachable full nodes
func (p *ConnectionPool) GetHealthyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, ok := range p.health {
		if ok {
			n++
		}
	}
	return n
}

func (p *ConnectionPool) monitorHealth() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		p.mu.Lock()
		for i, c := range p.clients {
			_, err := c.HealthCheck()
			p.health[i] = err == nil
			if err != nil {
				log.Printf("[POOL] health check failed for %s: %v", p.addrs[i], err)
			}
		}
		p.mu.Unlock()
	}
}

// Close shuts down all connections in the pool
func (p *ConnectionPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.clients {
		c.Close()
	}
}

// NewConnectionPoolForTest creates a ConnectionPool for testing (exports health map access)
func NewConnectionPoolForTest(clients []PoolClient, addrs []string) *ConnectionPool {
	p := &ConnectionPool{
		clients: clients,
		addrs:   addrs,
		health:  make(map[int]bool),
	}
	for i := range clients {
		p.health[i] = true
	}
	return p
}

// SetHealth sets the health status of a client at the given index (for testing)
func (p *ConnectionPool) SetHealth(idx int, healthy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.health[idx] = healthy
}

// GetHealth returns the health status of a client at the given index (for testing)
func (p *ConnectionPool) GetHealth(idx int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.health[idx]
}
