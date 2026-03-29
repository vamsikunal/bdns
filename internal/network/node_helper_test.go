package network

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	pb "github.com/bleasey/bdns/internal/proto/gatwaypb"
)

// mockPool satisfies the nxdomainPool interface for VerifyNXDOMAIN testing.
type mockPool struct {
	healthy   int
	failQuery bool
}

func (m *mockPool) GetHealthyCount() int { return m.healthy }
func (m *mockPool) QueryWithFailover(domain string, _ int64) (*pb.DomainQueryResponse, error) {
	if m.failQuery {
		return nil, errors.New("domain not found")
	}
	return &pb.DomainQueryResponse{DomainName: domain}, nil
}

func TestVerifyNXDOMAIN_Mock(t *testing.T) {
	n := &Node{}

	// nil pool → false
	if n.VerifyNXDOMAIN("a.bdns") {
		t.Error("expected false with nil ConnectionPool")
	}

	// < 2 healthy nodes → false even if query fails
	n.ConnectionPool = &mockPool{healthy: 1, failQuery: true}
	if n.VerifyNXDOMAIN("a.bdns") {
		t.Error("expected false when fewer than 2 healthy peers")
	}

	// 2+ healthy, query fails → all healthy nodes agree domain absent → true
	n.ConnectionPool = &mockPool{healthy: 2, failQuery: true}
	if !n.VerifyNXDOMAIN("a.bdns") {
		t.Error("expected true when 2 healthy nodes agree domain is absent")
	}

	// 2+ healthy, query succeeds → domain found → false
	n.ConnectionPool = &mockPool{healthy: 2, failQuery: false}
	if n.VerifyNXDOMAIN("a.bdns") {
		t.Error("expected false when domain is found on full node")
	}
}

func TestWaitForHeader_Success(t *testing.T) {
	n := &Node{}
	hdr := blockchain.BlockHeader{Index: 0, Hash: []byte("hash0")}

	// Add header after short delay to simulate network arrival
	go func() {
		time.Sleep(150 * time.Millisecond)
		n.AddBlockHeader(hdr)
	}()

	got := n.waitForHeader(0, 1*time.Second)
	if got == nil {
		t.Fatal("expected header to be found before timeout")
	}
	if got.Index != 0 {
		t.Errorf("expected Index=0, got %d", got.Index)
	}
}

func TestWaitForHeader_Timeout(t *testing.T) {
	n := &Node{}

	got := n.waitForHeader(0, 200*time.Millisecond)
	if got != nil {
		t.Error("expected nil when header never arrives")
	}
}

// buildMerkleProof constructs a minimal valid 2-leaf Merkle proof deterministically.
func buildMerkleProof() (txHash, merkleRoot []byte, proofPath [][]byte, directions []bool) {
	leaf := sha256.Sum256([]byte("test-tx-payload"))
	sibling := leaf // identical duplicate leaf (pads odd tree)
	root := sha256.Sum256(append(leaf[:], sibling[:]...))
	return leaf[:], root[:], [][]byte{sibling[:]}, []bool{true}
}

func TestHandleDNSProof_Valid(t *testing.T) {
	txHash, merkleRoot, proofPath, dirs := buildMerkleProof()

	headerHash := make([]byte, 32)
	copy(headerHash, []byte("test-header-hash"))

	hdr := blockchain.BlockHeader{Index: 0, Hash: headerHash, MerkleRoot: merkleRoot}
	n := &Node{IsFullNode: false, HeaderChain: []blockchain.BlockHeader{hdr}}

	// Clear any pre-existing cache entry for this domain
	domain := "valid-proof.bdns"
	delete(cache, cacheKey{Domain: domain, QueryType: "A"})

	response := DNSProofResponse{
		DomainName:  domain,
		Records:     []blockchain.Record{{Type: "A", Value: "2.3.4.5"}},
		BlockHeader: hdr,
		Proof: blockchain.MerkleProof{
			TxHash:     txHash,
			ProofPath:  proofPath,
			Directions: dirs,
		},
	}

	n.HandleDNSProof(response)

	records, found := GetFromCache(domain, "A")
	if !found {
		t.Fatal("expected cache hit after valid proof verification")
	}
	if len(records) == 0 || records[0].Value != "2.3.4.5" {
		t.Errorf("unexpected cached records: %v", records)
	}
}

func TestHandleDNSProof_TamperedProof(t *testing.T) {
	txHash, merkleRoot, proofPath, dirs := buildMerkleProof()

	// Tamper: flip a byte in the sibling so the proof no longer matches the root
	tampered := make([]byte, len(proofPath[0]))
	copy(tampered, proofPath[0])
	tampered[0] ^= 0xFF

	headerHash := make([]byte, 32)
	copy(headerHash, []byte("test-header-hash-tampered"))

	hdr := blockchain.BlockHeader{Index: 0, Hash: headerHash, MerkleRoot: merkleRoot}
	n := &Node{IsFullNode: false, HeaderChain: []blockchain.BlockHeader{hdr}}

	domain := "tampered-proof.bdns"
	delete(cache, cacheKey{Domain: domain, QueryType: "A"})

	response := DNSProofResponse{
		DomainName:  domain,
		Records:     []blockchain.Record{{Type: "A", Value: "9.9.9.9"}},
		BlockHeader: hdr,
		Proof: blockchain.MerkleProof{
			TxHash:     txHash,
			ProofPath:  [][]byte{tampered},
			Directions: dirs,
		},
	}

	n.HandleDNSProof(response)

	_, found := GetFromCache(domain, "A")
	if found {
		t.Error("cache must not be populated when Merkle proof is tampered")
	}
}
