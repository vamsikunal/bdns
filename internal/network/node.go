package network

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	cryptoRand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/consensus"
	"github.com/bleasey/bdns/internal/index"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/miekg/dns"
)

// headerBroadcaster is the minimal interface the node needs from GatewayServer.
// Defined here to avoid an import cycle between network and gateway packages.
type headerBroadcaster interface {
	BroadcastHeader(header *blockchain.BlockHeader)
}

// poolCloser is the minimal interface the node needs from ConnectionPool.
type poolCloser interface {
	Close()
}

// Node represents a blockchain peer
type Node struct {
	Address                 string
	Port                    int
	Config                  NodeConfig
	P2PNetwork              *P2PNetwork
	KeyPair                 *blockchain.KeyPair
	RegistryKeys            [][]byte
	SlotLeaders             map[int64][]byte // epoch to slot leader
	SlotMutex               sync.Mutex
	TransactionPool         map[int]*blockchain.Transaction
	TxMutex                 sync.Mutex
	IndexManager            *index.IndexManager
	Blockchain              *blockchain.Blockchain
	BcMutex                 sync.Mutex
	RandomNumber            []byte
	RandomMutex             sync.Mutex
	EpochRandoms            map[int64]map[string][]byte
	EpochCommitments        map[int64]map[string][]byte
	MySecrets               map[int64]consensus.SecretValues
	PendingReveals          map[int64]map[string]consensus.RevealData
	DRGDedupCache           map[string]uint64
	DRGDedupMutex           sync.Mutex
	ValidatorSetCacheAtomic atomic.Value // stores map[string]struct{} snapshots
	IsFullNode              bool         // full vs light node
	PeerID                  string
	KnownFullPeers          []string
	HeaderChain             []blockchain.BlockHeader // Light nodes store only headers
	GatewayServer           interface{}              // Full node gRPC server
	ConnectionPool          interface{}              // Light node gRPC connection pool
	cancel                  context.CancelFunc       // cancels CreateBlockIfLeader goroutine
	DNSServer               *dns.Server              // DNS server instance; closed by NodesCleanup
	BalanceLedger           *blockchain.BalanceLedger
	CommitStore             *blockchain.CommitStore
	StakeMap                blockchain.StakeStorer
	UnstakeQueue            *blockchain.UnstakeQueue
	SlashedEvidence         map[string]bool
	StakeMutex              sync.Mutex
	CurrentSlot             int64
	SlotSkipMutex           sync.Mutex
}

// Node Config
type NodeConfig struct {
	InitialTimestamp int64
	SlotInterval     int64
	SlotsPerEpoch    int64
	Seed             float64
}

type RandomNumberMsg struct {
	Epoch       int64
	SecretValue int    // u_i value
	RandomValue int    // r_i value
	Sender      []byte // Registry's public key
}

// NewNode initializes a blockchain node
func NewNode(ctx context.Context, addr string, topicName string, isFullNode bool) (*Node, error) {
	p2p, err := NewP2PNetwork(ctx, addr, topicName)
	if err != nil {
		return nil, err
	}

	node := &Node{
		Address:          p2p.Host.Addrs()[0].String(),
		P2PNetwork:       p2p,
		KeyPair:          blockchain.NewKeyPair(),
		SlotLeaders:      make(map[int64][]byte),
		TransactionPool:  make(map[int]*blockchain.Transaction),
		IndexManager:     index.NewIndexManager(),
		Blockchain:       nil,
		EpochRandoms:     make(map[int64]map[string][]byte),
		EpochCommitments: make(map[int64]map[string][]byte),
		MySecrets:        make(map[int64]consensus.SecretValues),
		PendingReveals:   make(map[int64]map[string]consensus.RevealData),
		DRGDedupCache:    make(map[string]uint64),
		IsFullNode:       isFullNode,
		StakeMap:         blockchain.NewStakeMap(),
		UnstakeQueue:     blockchain.NewUnstakeQueue(),
		SlashedEvidence:  make(map[string]bool),
		PeerID:           p2p.Host.ID().String(),
		KnownFullPeers:   []string{},
	}

	go node.ListenForDirectMessages()
	go node.P2PNetwork.ListenForGossip()
	go node.HandleGossip()
	return node, nil
}

func (n *Node) GenerateRandomNumber() []byte {
	n.RandomMutex.Lock()
	defer n.RandomMutex.Unlock()

	randomBytes := make([]byte, 32)
	if _, err := cryptoRand.Read(randomBytes); err != nil {
		log.Panic("Failed to generate random number:", err)
	}

	n.RandomNumber = randomBytes
	return randomBytes
}

func (n *Node) HandleMsgGivenType(msg GossipEnvelope, _ *DNSMetrics) {
	// This includes messages received from BOTH direct and broadcast mode
	// since for now there is no difference in handling based on the mode of reception
	dnsMetrics := GetDNSMetrics()

	switch msg.Type {
	case DNSRequest:
		var req BDNSRequest
		fmt.Println("DNS Request received at ", n.Address, " -> ", req.DomainName)
		err := json.Unmarshal(msg.Content, &req)
		if err != nil {
			log.Println("Failed during unmarshalling")
		}
		n.DNSRequestHandler(req, msg.Sender, dnsMetrics)

	case DNSResponse:
		var res BDNSResponse
		err := json.Unmarshal(msg.Content, &res)
		if err != nil {
			log.Println("Failed during unmarshalling")
		}
		n.DNSResponseHandler(res)

	case MsgTransaction:
		var tx blockchain.Transaction
		err := json.Unmarshal(msg.Content, &tx)
		if err != nil {
			log.Println("Failed during unmarshalling")
		}
		n.AddTransaction(&tx)

	case MsgBlock:
		var block blockchain.Block
		err := json.Unmarshal(msg.Content, &block)
		if err != nil {
			log.Println("Failed during unmarshalling")
		}
		n.AddBlock(&block)

	case MsgCommitment:
		var cMsg CommitmentMsg
		if err := json.Unmarshal(msg.Content, &cMsg); err != nil {
			log.Println("Failed to unmarshal CommitmentMsg:", err)
			break
		}
		if err := n.CommitmentHandler(cMsg); err != nil {
			log.Println("CommitmentHandler error:", err)
		}

	case MsgReveal:
		var rMsg RevealMsg
		if err := json.Unmarshal(msg.Content, &rMsg); err != nil {
			log.Println("Failed to unmarshal RevealMsg:", err)
			break
		}
		if err := n.RevealHandler(rMsg); err != nil {
			log.Println("RevealHandler error:", err)
		}

	case MsgSlotSkip:
		var skipMsg SlotSkipAttestation
		if err := json.Unmarshal(msg.Content, &skipMsg); err != nil {
			log.Println("Failed to unmarshal SlotSkipAttestation:", err)
			break
		}
		n.handleSlotSkip(skipMsg)

	case MsgInv:
		n.HandleINV(msg.Sender)

	case MsgGetData:
		n.HandleGetData(msg.Sender)

	case MsgGetBlock:
		n.HandleGetBlock(msg.Sender)

	case MsgDNSQuery:
		var query DNSQueryMsg
		err := json.Unmarshal(msg.Content, &query)
		if err != nil {
			log.Println("Failed to unmarshal DNS query:", err)
		}
		n.HandleDNSQuery(msg.Sender, query)

	case MsgDNSProof:
		var response DNSProofResponse
		err := json.Unmarshal(msg.Content, &response)
		if err != nil {
			log.Println("Failed to unmarshal DNS proof:", err)
		}
		n.HandleDNSProof(response)
	}
}

// HandleGossip listens for messages from the gossip network
func (n *Node) HandleGossip() {
	for msg := range n.P2PNetwork.MsgChan {
		if msg.Metrics != nil {
			n.HandleMsgGivenType(msg, msg.Metrics)
		} else {
			n.HandleMsgGivenType(msg, nil)
		}
	}

	fmt.Println("Exiting gossip listener for ", n.Address)
}

// Handles direct peer-to-peer messages
func (n *Node) ListenForDirectMessages() {
	// Handler for dns response
	n.P2PNetwork.Host.SetStreamHandler("/dns-response", func(s network.Stream) {
		defer s.Close()
		var msg GossipEnvelope
		if err := json.NewDecoder(s).Decode(&msg); err != nil {
			log.Println("Error decoding direct response:", err)
			return
		}

		n.HandleMsgGivenType(msg, nil)
	})

	// Handler for direct transaction forwarding (fast path)
	n.P2PNetwork.Host.SetStreamHandler("/tx-forward", func(s network.Stream) {
		defer s.Close()
		data, err := io.ReadAll(s)
		if err != nil {
			return
		}
		var tx blockchain.Transaction
		if err := json.Unmarshal(data, &tx); err != nil {
			return
		}
		n.AddTransaction(&tx)
	})
}

func (n *Node) BroadcastTransaction(tx blockchain.Transaction) {
	n.AddTransaction(&tx)
	// Concurrent hybrid routing: gossip (censorship resistance) +
	// direct stream to known peers (bounded-latency fast path).
	go func() {
		n.P2PNetwork.BroadcastMessage(MsgTransaction, tx, nil)
	}()
	go n.forwardToPeers(tx)
}

// forwardToPeers pushes a transaction directly to all known full peers via a
// dedicated libp2p stream — bypassing gossip propagation delay.
func (n *Node) forwardToPeers(tx blockchain.Transaction) {
	data, err := json.Marshal(tx)
	if err != nil {
		return
	}
	for _, peerID := range n.KnownFullPeers {
		peerID := peerID
		go func() {
			id, err := peer.Decode(peerID)
			if err != nil {
				return
			}
			stream, err := n.P2PNetwork.Host.NewStream(context.Background(), id, "/tx-forward")
			if err != nil {
				return
			}
			defer stream.Close()
			_, _ = stream.Write(data)
		}()
	}
}

func (n *Node) MakeDNSRequest(domainName string, metrics *DNSMetrics) {
	if records, found := GetFromCache(domainName, "A"); found {
		if len(records) > 0 {
			fmt.Printf("[CACHE HIT] %s -> %s\n", domainName, records[0].Value)
		}
		return
	}
	req := BDNSRequest{DomainName: domainName}
	n.P2PNetwork.BroadcastMessage(DNSRequest, req, metrics)
}

// HasSeenDRGMessage returns true if this message key was already processed for this epoch.
// Run before ECDSA verification to drop replay floods cheaply.
func (n *Node) HasSeenDRGMessage(msgKey string, epoch uint64) bool {
	n.DRGDedupMutex.Lock()
	defer n.DRGDedupMutex.Unlock()
	cachedEpoch, ok := n.DRGDedupCache[msgKey]
	return ok && cachedEpoch == epoch
}

// MarkDRGMessageSeen records a message key as seen for this epoch.
func (n *Node) MarkDRGMessageSeen(msgKey string, epoch uint64) {
	n.DRGDedupMutex.Lock()
	n.DRGDedupCache[msgKey] = epoch
	n.DRGDedupMutex.Unlock()
}

// CommitmentHandler stores a received commitment and flushes any buffered out-of-order reveals.
func (n *Node) CommitmentHandler(msg CommitmentMsg) error {
	// Dedup before expensive signature verification
	msgKey := fmt.Sprintf("commit:%d:%s", msg.Epoch, msg.Sender)
	if n.HasSeenDRGMessage(msgKey, uint64(msg.Epoch)) {
		return nil
	}
	// Verify ECDSA signature
	pubKeyBytes, err := hex.DecodeString(msg.Sender)
	if err != nil {
		return fmt.Errorf("CommitmentHandler: invalid sender hex: %w", err)
	}
	epochBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBuf, uint64(msg.Epoch))
	payload := append(epochBuf, msg.Commitment...)
	payload = append(payload, []byte(msg.Sender)...)
	h := sha256.Sum256(payload)
	pub, err := blockchain.BytesToPublicKey(pubKeyBytes)
	if err != nil || !ecdsa.VerifyASN1(pub, h[:], msg.Signature) {
		return fmt.Errorf("CommitmentHandler: invalid signature from %s", msg.Sender[:8])
	}
	n.MarkDRGMessageSeen(msgKey, uint64(msg.Epoch))

	n.RandomMutex.Lock()
	if n.EpochCommitments[msg.Epoch] == nil {
		n.EpochCommitments[msg.Epoch] = make(map[string][]byte)
	}
	n.EpochCommitments[msg.Epoch][msg.Sender] = msg.Commitment

	// Flush buffered out-of-order reveals from PendingReveals
	pending := n.PendingReveals[msg.Epoch]
	pendingForSender := pending[msg.Sender]
	n.RandomMutex.Unlock()

	if pendingForSender.U != nil {
		sv := consensus.SecretValues{U: pendingForSender.U, R: pendingForSender.R, Commitment: msg.Commitment}
		if consensus.VerifyCommitment(pubKeyBytes, sv) {
			n.RandomMutex.Lock()
			if n.EpochRandoms[msg.Epoch] == nil {
				n.EpochRandoms[msg.Epoch] = make(map[string][]byte)
			}
			n.EpochRandoms[msg.Epoch][msg.Sender] = pendingForSender.U
			delete(n.PendingReveals[msg.Epoch], msg.Sender)
			n.RandomMutex.Unlock()
		}
	}
	return nil
}

// RevealHandler verifies and stores a reveal, or buffers it if no commitment received yet.
func (n *Node) RevealHandler(msg RevealMsg) error {
	// Dedup before expensive signature verification
	msgKey := fmt.Sprintf("reveal:%d:%s", msg.Epoch, msg.Sender)
	if n.HasSeenDRGMessage(msgKey, uint64(msg.Epoch)) {
		return nil
	}
	// Verify ECDSA signature
	pubKeyBytes, err := hex.DecodeString(msg.Sender)
	if err != nil {
		return fmt.Errorf("RevealHandler: invalid sender hex: %w", err)
	}
	epochBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBuf, uint64(msg.Epoch))
	payload := append(epochBuf, msg.U...)
	payload = append(payload, msg.R...)
	payload = append(payload, []byte(msg.Sender)...)
	h := sha256.Sum256(payload)
	pub, err := blockchain.BytesToPublicKey(pubKeyBytes)
	if err != nil || !ecdsa.VerifyASN1(pub, h[:], msg.Signature) {
		return fmt.Errorf("RevealHandler: invalid signature from %s", msg.Sender[:8])
	}
	n.MarkDRGMessageSeen(msgKey, uint64(msg.Epoch))

	n.RandomMutex.Lock()
	commitment := n.EpochCommitments[msg.Epoch][msg.Sender]
	n.RandomMutex.Unlock()

	if commitment == nil {
		// Buffer reveal — commitment hasn't arrived yet
		n.RandomMutex.Lock()
		if n.PendingReveals[msg.Epoch] == nil {
			n.PendingReveals[msg.Epoch] = make(map[string]consensus.RevealData)
		}
		n.PendingReveals[msg.Epoch][msg.Sender] = consensus.RevealData{U: msg.U, R: msg.R}
		n.RandomMutex.Unlock()
		return nil
	}

	sv := consensus.SecretValues{U: msg.U, R: msg.R, Commitment: commitment}
	if !consensus.VerifyCommitment(pubKeyBytes, sv) {
		return fmt.Errorf("RevealHandler: commitment mismatch from %s", msg.Sender[:8])
	}

	n.RandomMutex.Lock()
	if n.EpochRandoms[msg.Epoch] == nil {
		n.EpochRandoms[msg.Epoch] = make(map[string][]byte)
	}
	n.EpochRandoms[msg.Epoch][msg.Sender] = msg.U
	n.RandomMutex.Unlock()
	return nil
}

// RebuildValidatorSetCache publishes a fresh immutable validator set snapshot
// via ValidatorSetCacheAtomic so P2P goroutines can check without lock contention.
func (n *Node) RebuildValidatorSetCache(stakeMap blockchain.StakeStorer) {
	if stakeMap == nil {
		return
	}
	snap := make(map[string]struct{})
	for k := range stakeMap.GetAll() {
		snap[k] = struct{}{}
	}
	n.ValidatorSetCacheAtomic.Store(snap)
}

// IsKnownValidator checks the atomic validator snapshot (no mutex, safe for concurrent reads).
func (n *Node) IsKnownValidator(pubKeyHex string) bool {
	v := n.ValidatorSetCacheAtomic.Load()
	if v == nil {
		return false
	}
	snap := v.(map[string]struct{})
	_, ok := snap[pubKeyHex]
	return ok
}

// PruneDRGEpochState deletes epoch state older than currentEpoch-2 to prevent unbounded growth.
func (n *Node) PruneDRGEpochState(currentEpoch int64) {
	n.RandomMutex.Lock()
	defer n.RandomMutex.Unlock()
	for epoch := range n.EpochRandoms {
		if epoch < currentEpoch-2 {
			delete(n.EpochRandoms, epoch)
		}
	}
	for epoch := range n.EpochCommitments {
		if epoch < currentEpoch-2 {
			delete(n.EpochCommitments, epoch)
		}
	}
	for epoch := range n.MySecrets {
		if epoch < currentEpoch-2 {
			delete(n.MySecrets, epoch)
		}
	}
	for epoch := range n.PendingReveals {
		if epoch < currentEpoch-2 {
			delete(n.PendingReveals, epoch)
		}
	}
	// Prune dedup cache entries older than currentEpoch-2
	n.DRGDedupMutex.Lock()
	for k, ep := range n.DRGDedupCache {
		if int64(ep) < currentEpoch-2 {
			delete(n.DRGDedupCache, k)
		}
	}
	n.DRGDedupMutex.Unlock()
}

// BroadcastCommitment runs the commitment phase for epoch and broadcasts
// the commitment hash to all peers. U and R are stored locally and never sent here.
func (n *Node) BroadcastCommitment(epoch int64) error {
	sv, err := consensus.CommitmentPhase(n.KeyPair.PublicKey)
	if err != nil {
		return fmt.Errorf("BroadcastCommitment: %w", err)
	}

	n.RandomMutex.Lock()
	n.MySecrets[epoch] = sv
	n.RandomMutex.Unlock()

	senderHex := hex.EncodeToString(n.KeyPair.PublicKey)
	msg := CommitmentMsg{
		Epoch:      epoch,
		Commitment: sv.Commitment,
		Sender:     senderHex,
	}

	// Sign: SHA-256(epoch bytes || commitment || sender)
	epochBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBuf, uint64(epoch))
	payload := append(epochBuf, sv.Commitment...)
	payload = append(payload, []byte(senderHex)...)
	h := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(cryptoRand.Reader, &n.KeyPair.PrivateKey, h[:])
	if err != nil {
		return fmt.Errorf("BroadcastCommitment: sign failed: %w", err)
	}
	msg.Signature = sig

	n.P2PNetwork.BroadcastMessage(MsgCommitment, msg, nil)
	return nil
}

// BroadcastReveal broadcasts the reveal message for epoch, exposing U and R so
// peers can verify the commitment and extract the randomness contribution.
func (n *Node) BroadcastReveal(epoch int64) error {
	n.RandomMutex.Lock()
	sv, ok := n.MySecrets[epoch]
	n.RandomMutex.Unlock()
	if !ok {
		return fmt.Errorf("BroadcastReveal: no commitment found for epoch %d", epoch)
	}

	senderHex := hex.EncodeToString(n.KeyPair.PublicKey)
	msg := RevealMsg{
		Epoch:  epoch,
		U:      sv.U,
		R:      sv.R,
		Sender: senderHex,
	}

	// Sign: SHA-256(epoch bytes || U || R || sender)
	epochBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBuf, uint64(epoch))
	payload := append(epochBuf, sv.U...)
	payload = append(payload, sv.R...)
	payload = append(payload, []byte(senderHex)...)
	h := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(cryptoRand.Reader, &n.KeyPair.PrivateKey, h[:])
	if err != nil {
		return fmt.Errorf("BroadcastReveal: sign failed: %w", err)
	}
	msg.Signature = sig

	n.P2PNetwork.BroadcastMessage(MsgReveal, msg, nil)
	return nil
}

func (n *Node) DNSRequestHandler(req BDNSRequest, reqSender string, metrics *DNSMetrics) {
	start := time.Now()

	// If this node is a light node, ask full node for answer + proof
	if !n.IsFullNode {
		fmt.Println("Light node requesting DNS proof for:", req.DomainName)
		query := DNSQueryMsg{DomainName: req.DomainName}
		for _, peerID := range n.KnownFullPeers {
			if peerID != reqSender {
				n.P2PNetwork.DirectMessage(MsgDNSQuery, query, peerID)
				fmt.Println(" Light node sent DNS query to:", peerID)
				if metrics != nil {
					metrics.AddLightNodeForwardedResolution(time.Since(start))
				}
				break
			}
		}
		return
	}

	// Otherwise, full node will handle it
	n.TxMutex.Lock()
	defer n.TxMutex.Unlock()
	tx := n.IndexManager.GetDomain(req.DomainName)

	if metrics != nil {
		metrics.AddFullNodeDirectResolution(time.Since(start))
	}
	if tx != nil {
		// Only resolve domains in the "active" phase
		slotsPerDay := int64(86400) / n.Config.SlotInterval
		currentSlot := (time.Now().Unix() - n.Config.InitialTimestamp) / n.Config.SlotInterval
		phase := blockchain.GetDomainPhase(currentSlot, tx.ExpirySlot, slotsPerDay)
		if phase != "active" {
			fmt.Printf("[DNS] Domain %s is in %s phase, not resolving\n", req.DomainName, phase)
			return
		}
		// Full multi-record resolution via DNSProofResponse is handled in HandleDNSQuery.
		firstA := ""
		for _, r := range tx.Records {
			if r.Type == "A" {
				firstA = r.Value
				break
			}
		}
		res := BDNSResponse{
			Timestamp:  tx.Timestamp,
			DomainName: tx.DomainName,
			IP:         firstA,
			CacheTTL:   tx.CacheTTL,
			OwnerKey:   tx.OwnerKey,
			Signature:  tx.Signature,
		}
		n.P2PNetwork.DirectMessage(DNSResponse, res, reqSender)
	}
	fmt.Println("DNS Request received at ", n.Address, " -> ", req.DomainName)
}

func (n *Node) DNSResponseHandler(res BDNSResponse) {
	fmt.Println("DNS Response with Full node received at ", n.Address, " ->", res.DomainName, " IP:", res.IP)
	if res.IP != "" {
		SetToCache(res.DomainName, "A", []blockchain.Record{{Type: "A", Value: res.IP, Priority: 0}})
	}
}

func (n *Node) RandomNumberHandler(epoch int64, sender string, _ int, _ int) {
	n.RandomMutex.Lock()
	defer n.RandomMutex.Unlock()
	if n.EpochRandoms[epoch] == nil {
		n.EpochRandoms[epoch] = make(map[string][]byte)
	}
	// U field not available via old int path — entry stored empty until replaced by CommitmentHandler
	if _, exists := n.EpochRandoms[epoch][sender]; !exists {
		n.EpochRandoms[epoch][sender] = nil
	}
}

// AddBlockHeader stores a block header for light nodes (chain extension with validation)
func (n *Node) AddBlockHeader(header blockchain.BlockHeader) {
	n.BcMutex.Lock()
	defer n.BcMutex.Unlock()

	if len(n.HeaderChain) > 0 {
		latest := n.HeaderChain[len(n.HeaderChain)-1]
		// Silently drop exact duplicates (same header broadcast by multiple full-node streams).
		if bytes.Equal(header.Hash, latest.Hash) {
			return
		}
		if !bytes.Equal(header.PrevHash, latest.Hash) {
			log.Println("Header doesn't extend chain, skipping")
			return
		}
	}
	n.HeaderChain = append(n.HeaderChain, header)
}

type SlotSkipAttestation struct {
	Slot      int64  `json:"slot"`
	LeaderKey []byte `json:"leader_key"`
	Signature []byte `json:"signature"`
}

// BroadcastSlotSkip sends a signed attestation that this slot has no transactions.
func (n *Node) BroadcastSlotSkip(slot int64) {
	attestation := SlotSkipAttestation{
		Slot:      slot,
		LeaderKey: n.KeyPair.PublicKey,
	}
	payload := append([]byte("slot-skip:"), blockchain.IntToByteArr(slot)...)
	payload = append(payload, n.KeyPair.PublicKey...)
	h := sha256.Sum256(payload)
	sig, _ := ecdsa.SignASN1(cryptoRand.Reader, &n.KeyPair.PrivateKey, h[:])
	attestation.Signature = sig

	n.P2PNetwork.BroadcastMessage(MsgSlotSkip, attestation, nil)
}

// handleSlotSkip validates and records a slot-skip attestation from the leader.
func (n *Node) handleSlotSkip(skip SlotSkipAttestation) {
	// Derive epoch from slot to look up the expected leader.
	epoch := skip.Slot / n.Config.SlotsPerEpoch
	expectedLeader := n.GetSlotLeader(epoch)
	if !bytes.Equal(skip.LeaderKey, expectedLeader) {
		log.Printf("[SLOT-SKIP] slot %d: signer is not the slot leader, ignoring", skip.Slot)
		return
	}
	payload := append([]byte("slot-skip:"), blockchain.IntToByteArr(skip.Slot)...)
	payload = append(payload, skip.LeaderKey...)
	h := sha256.Sum256(payload)
	pubKey, err := blockchain.BytesToPublicKey(skip.LeaderKey)
	if err != nil {
		return
	}
	if !ecdsa.VerifyASN1(pubKey, h[:], skip.Signature) {
		log.Printf("[SLOT-SKIP] slot %d: invalid signature, ignoring", skip.Slot)
		return
	}
	n.SlotSkipMutex.Lock()
	if skip.Slot > n.CurrentSlot {
		n.CurrentSlot = skip.Slot
	}
	n.SlotSkipMutex.Unlock()
}
