package gateway

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/network"
	pb "github.com/bleasey/bdns/internal/proto/gatwaypb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// GatewayClient manages gRPC connections from a light node to one full node
type GatewayClient struct {
	node   *network.Node
	addr   string
	conn   *grpc.ClientConn
	client pb.BDNSGatewayClient
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.RWMutex
}

// newGatewayClient opens a connection to a single full node and starts header streaming
func newGatewayClient(addr string) poolClient {
	return newGatewayClientForNode(nil, addr)
}

// NewGatewayClientForNode creates a client that delivers headers to a light node's HeaderChain
func NewGatewayClientForNode(node *network.Node, addr string) *GatewayClient {
	return newGatewayClientForNode(node, addr)
}

func newGatewayClientForNode(node *network.Node, addr string) *GatewayClient {
	ctx, cancel := context.WithCancel(context.Background())

	// grpc.NewClient is the non-deprecated replacement for grpc.Dial (deprecated in v1.63+)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[gRPC] failed to connect to %s: %v", addr, err)
		cancel()
		return nil
	}

	c := &GatewayClient{
		node:   node,
		addr:   addr,
		conn:   conn,
		client: pb.NewBDNSGatewayClient(conn),
		ctx:    ctx,
		cancel: cancel,
	}

	if node != nil {
		go c.streamHeaders()
	}

	log.Printf("[gRPC] connected to full node: %s", addr)
	return c
}

// streamHeaders subscribes to the full node's header stream and appends each
// received header to the light node's HeaderChain via AddBlockHeader.
func (c *GatewayClient) streamHeaders() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		c.mu.RLock()
		cl := c.client
		c.mu.RUnlock()

		// Snapshot current chain length so the server sends only missing headers
		c.node.BcMutex.Lock()
		startIdx := int64(len(c.node.HeaderChain))
		c.node.BcMutex.Unlock()

		stream, err := cl.SubscribeHeaders(c.ctx, &pb.SubscribeRequest{StartIndex: startIdx})
		if err != nil {
			log.Printf("[gRPC] stream to %s failed: %v — retrying in 1s", c.addr, err)
			time.Sleep(time.Second)
			continue
		}

		for {
			hdr, err := stream.Recv()
			if err != nil {
				log.Printf("[gRPC] stream recv from %s: %v", c.addr, err)
				break
			}
			c.node.AddBlockHeader(blockchain.BlockHeader{
				Index:      hdr.Index,
				SlotNumber: hdr.SlotNumber,
				Hash:       hdr.Hash,
				PrevHash:   hdr.PrevHash,
				MerkleRoot: hdr.MerkleRoot,
				IndexHash:  hdr.IndexHash,
			})
		}

		time.Sleep(time.Second)
	}
}

// QueryDomain resolves a domain name via the full node's gRPC service
func (c *GatewayClient) QueryDomain(domain string, blockIndex int64) (*pb.DomainQueryResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client.QueryDomain(context.Background(), &pb.DomainQueryRequest{
		DomainName: domain,
		BlockIndex: blockIndex,
	})
}

// HealthCheck probes the full node for liveness
func (c *GatewayClient) HealthCheck() (*pb.HealthCheckResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client.HealthCheck(context.Background(), &pb.HealthCheckRequest{})
}

// Close shuts down the gRPC connection and cancels the streaming goroutine
func (c *GatewayClient) Close() {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
	}
}

// NewConnectionPoolFromAddrs creates a ConnectionPool from a list of full node addresses.
func NewConnectionPoolFromAddrs(node *network.Node, addrs []string) *ConnectionPool {
	clients := make([]poolClient, len(addrs))
	for i, addr := range addrs {
		clients[i] = NewGatewayClientForNode(node, addr)
	}
	return NewConnectionPool(clients, addrs)
}
