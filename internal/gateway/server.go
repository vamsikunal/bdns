package gateway

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/network"
	pb "github.com/bleasey/bdns/internal/proto/gatwaypb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// GatewayServer implements the BDNSGateway gRPC service on full nodes
type GatewayServer struct {
	pb.UnimplementedBDNSGatewayServer
	node        *network.Node
	grpcServer  *grpc.Server
	subscribers map[string]chan *pb.BlockHeader // keyed by peer address
	mu          sync.RWMutex
}

// NewGatewayServer starts a gRPC server on the given port and returns the handle
func NewGatewayServer(node *network.Node, port int) *GatewayServer {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("[gRPC] failed to listen on port %d: %v", port, err)
	}

	s := &GatewayServer{
		node:        node,
		grpcServer:  grpc.NewServer(),
		subscribers: make(map[string]chan *pb.BlockHeader),
	}

	pb.RegisterBDNSGatewayServer(s.grpcServer, s)

	go func() {
		log.Printf("[gRPC] server started on port %d", port)
		if err := s.grpcServer.Serve(lis); err != nil {
			log.Printf("[gRPC] server stopped: %v", err)
		}
	}()

	return s
}

// SubscribeHeaders streams block headers to a connecting light node.
func (s *GatewayServer) SubscribeHeaders(
	req *pb.SubscribeRequest,
	stream pb.BDNSGateway_SubscribeHeadersServer,
) error {
	var clientKey string
	if p, ok := peer.FromContext(stream.Context()); ok {
		clientKey = p.Addr.String()
	}

	ch := make(chan *pb.BlockHeader, 100)

	s.mu.Lock()
	s.subscribers[clientKey] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.subscribers, clientKey)
		close(ch)
		s.mu.Unlock()
	}()

	// Replay historical headers the light node has not seen yet
	if req.StartIndex > 0 {
		s.node.BcMutex.Lock()
		tip := s.node.Blockchain.GetLatestBlock().Index
		s.node.BcMutex.Unlock()

		for i := req.StartIndex; i <= tip; i++ {
			s.node.BcMutex.Lock()
			block := s.node.Blockchain.GetBlockByIndex(i)
			s.node.BcMutex.Unlock()

			if block == nil {
				continue
			}
			h := block.Header()
			if err := stream.Send(toProtoHeader(&h)); err != nil {
				return err
			}
		}
	}

	// Forward live headers until the client disconnects
	for {
		select {
		case hdr := <-ch:
			if err := stream.Send(hdr); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// QueryDomain resolves a domain name and returns its IP address with a Merkle proof.
// The proof allows the caller to verify inclusion in the block without downloading the full block.
func (s *GatewayServer) QueryDomain(
	ctx context.Context,
	req *pb.DomainQueryRequest,
) (*pb.DomainQueryResponse, error) {
	// Snapshot domain record under lock; release before heavy work
	s.node.TxMutex.Lock()
	tx := s.node.IndexManager.GetDomain(req.DomainName)
	if tx == nil {
		s.node.TxMutex.Unlock()
		return nil, status.Errorf(codes.NotFound, "domain not found: %s", req.DomainName)
	}

	expirySlot := tx.ExpirySlot
	var ipAddress string
	for _, r := range tx.Records {
		if r.Type == "A" {
			ipAddress = r.Value
			break
		}
	}

	loc := s.node.IndexManager.GetTxLocation(req.DomainName)
	s.node.TxMutex.Unlock()

	// Validate domain phase
	currentSlot := (time.Now().Unix() - s.node.Config.InitialTimestamp) / s.node.Config.SlotInterval
	slotsPerDay := int64(86400) / s.node.Config.SlotInterval
	phase := blockchain.GetDomainPhase(currentSlot, expirySlot, slotsPerDay)
	if phase != "active" {
		return nil, status.Errorf(codes.NotFound, "domain not active: %s (phase=%s)", req.DomainName, phase)
	}

	if loc == nil {
		return nil, status.Errorf(codes.Internal, "tx location missing for %s", req.DomainName)
	}

	// Fetch block and generate Merkle proof
	s.node.BcMutex.Lock()
	block := s.node.Blockchain.GetBlockByIndex(loc.BlockIndex)
	s.node.BcMutex.Unlock()

	if block == nil {
		return nil, status.Errorf(codes.Internal, "block %d not found", loc.BlockIndex)
	}

	proof := block.GenerateMerkleProof(loc.TxIndex)
	if proof == nil {
		return nil, status.Errorf(codes.Internal, "failed to generate Merkle proof")
	}

	h := block.Header()

	return &pb.DomainQueryResponse{
		DomainName:  req.DomainName,
		IpAddress:   ipAddress,
		Proof:       &pb.MerkleProof{
			TxHash:    proof.TxHash,
			ProofPath: proof.ProofPath,
			Directions: proof.Directions,
		},
		BlockHeader: toProtoHeader(&h),
	}, nil
}

// HealthCheck reports the node's readiness and current chain state
func (s *GatewayServer) HealthCheck(
	ctx context.Context,
	req *pb.HealthCheckRequest,
) (*pb.HealthCheckResponse, error) {
	s.node.BcMutex.Lock()
	height := s.node.Blockchain.GetLatestBlock().Index
	s.node.BcMutex.Unlock()

	peers := s.node.P2PNetwork.Host.Network().Peers()

	return &pb.HealthCheckResponse{
		Healthy:     true,
		ChainHeight: height,
		PeerCount:   int32(len(peers)),
	}, nil
}

// BroadcastHeader delivers a new block header to all connected light node subscribers.
func (s *GatewayServer) BroadcastHeader(header *blockchain.BlockHeader) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pbHdr := toProtoHeader(header)
	for addr, ch := range s.subscribers {
		select {
		case ch <- pbHdr:
		default:
			log.Printf("[gRPC] subscriber %s is slow, dropping header %d", addr, header.Index)
		}
	}
}

// Close performs a graceful shutdown of the gRPC server
func (s *GatewayServer) Close() {
	s.grpcServer.GracefulStop()
}

// toProtoHeader converts a blockchain.BlockHeader to its proto representation
func toProtoHeader(h *blockchain.BlockHeader) *pb.BlockHeader {
	return &pb.BlockHeader{
		Index:      h.Index,
		SlotNumber: h.SlotNumber,
		Hash:       h.Hash,
		PrevHash:   h.PrevHash,
		MerkleRoot: h.MerkleRoot,
		IndexHash:  h.IndexHash,
	}
}
