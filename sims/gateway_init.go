package sims

import (
	"fmt"

	"github.com/bleasey/bdns/internal/gateway"
	"github.com/bleasey/bdns/internal/network"
)



// grpcBasePort is the starting port for full-node gRPC servers
const grpcBasePort = 50051

// InitGateway wires GatewayServer to every full node and ConnectionPool to
// every light node. It must be called after InitializeP2PNodes returns.
func InitGateway(nodes []*network.Node) {
	var fullNodeAddrs []string

	// Start gRPC servers on all full nodes and collect their addresses
	for i, node := range nodes {
		if node.IsFullNode {
			port := grpcBasePort + i
			node.GatewayServer = gateway.NewGatewayServer(node, port)
			fullNodeAddrs = append(fullNodeAddrs, fmt.Sprintf("localhost:%d", port))
			fmt.Printf("[gRPC] server started on node %d at port %d\n", i+1, port)
		}
	}

	// Connect light nodes to all full nodes via ConnectionPool
	for i, node := range nodes {
		if !node.IsFullNode {
			node.ConnectionPool = gateway.NewConnectionPoolFromAddrs(node, fullNodeAddrs)
			fmt.Printf("[gRPC] light node %d connected to %d full nodes\n", i+1, len(fullNodeAddrs))
		}
	}
}

// CloseGateway gracefully shuts down all gRPC servers and connection pools
func CloseGateway(nodes []*network.Node) {
	for _, node := range nodes {
		if node.IsFullNode {
			if gs, ok := node.GatewayServer.(*gateway.GatewayServer); ok {
				gs.Close()
			}
		} else {
			if cp, ok := node.ConnectionPool.(*gateway.ConnectionPool); ok {
				cp.Close()
			}
		}
	}
}
