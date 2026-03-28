package network

import (
	"bytes"
	"log"
	"sort"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/gateway"
	"github.com/bleasey/bdns/internal/index"
	pb "github.com/bleasey/bdns/internal/proto/gatwaypb"
)



func copyStringBoolMap(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (n *Node) AddBlock(block *blockchain.Block) {
	epoch := (block.Timestamp - n.Config.InitialTimestamp) / (n.Config.SlotInterval * n.Config.SlotsPerEpoch)
	slotLeader := n.GetSlotLeader(epoch)
	slotsPerDay := int64(86400) / n.Config.SlotInterval

	if block.Index == 0 {
		if !blockchain.ValidateGenesisBlock(block, n.RegistryKeys, slotLeader) {
			log.Println("Invalid genesis block received at", n.Address)
			return
		}
	} else {
		imOverlay := index.NewIndexOverlay(n.IndexManager)

		n.StakeMutex.Lock()
		stagingStake := n.StakeMap.Clone()
		stagingQueue := n.UnstakeQueue.Clone()
		stagingEvidence := copyStringBoolMap(n.SlashedEvidence)
		n.StakeMutex.Unlock()

		staging, newStakeMap, newQueue, newEvidence, commitOverlay, ok := blockchain.ValidateBlock(
			block, n.Blockchain.GetLatestBlock(),
			slotLeader, n.BalanceLedger, imOverlay, n.IndexManager, slotsPerDay,
			stagingStake, stagingQueue, stagingEvidence, n.CommitStore)
		if !ok {
			log.Println("Invalid block received at", n.Address)
			return
		}
		n.BalanceLedger = staging
		if commitOverlay != nil && n.CommitStore != nil {
			commitOverlay.Commit(n.CommitStore)
		}
		imOverlay.Commit()

		n.StakeMutex.Lock()
		n.StakeMap = newStakeMap
		n.UnstakeQueue = newQueue
		n.SlashedEvidence = newEvidence
		n.StakeMutex.Unlock()
		n.RebuildValidatorSetCache(n.StakeMap)
	}

	n.TxMutex.Lock()
	blockchain.RemoveTxsFromPool(block.Transactions, n.TransactionPool)
	n.TxMutex.Unlock()

	n.BcMutex.Lock()
	n.Blockchain.AddBlock(block)
	for _, tx := range block.Transactions {
		if tx.RedeemsTxID != 0 && (tx.Type == blockchain.UPDATE ||
			tx.Type == blockchain.RENEW || tx.Type == blockchain.REVOKE) {
			n.Blockchain.MarkAsSpent(tx.RedeemsTxID)
		}
	}
	n.BcMutex.Unlock()

	if n.IsFullNode {
		if gs, ok := n.GatewayServer.(headerBroadcaster); ok {
			header := block.Header()
			gs.BroadcastHeader(&header)
		}
	} else {
		n.AddBlockHeader(block.Header())
	}
}


func (n *Node) AddTransaction(tx *blockchain.Transaction) {
	n.TxMutex.Lock()
	defer n.TxMutex.Unlock()

	// Build candidate list: existing pool + new tx
	pendingList := make([]blockchain.Transaction, 0, len(n.TransactionPool)+1)
	for _, ptx := range n.TransactionPool {
		pendingList = append(pendingList, *ptx)
	}
	pendingList = append(pendingList, *tx)

	// Sort by nonce to ensure correct shadow map accumulation
	sort.Slice(pendingList, func(i, j int) bool {
		if pendingList[i].Nonce != pendingList[j].Nonce {
			return pendingList[i].Nonce < pendingList[j].Nonce
		}
		return pendingList[i].TID < pendingList[j].TID
	})

	if n.Config.SlotInterval == 0 {
		return
	}
	currentSlot := (time.Now().Unix() - n.Config.InitialTimestamp) / n.Config.SlotInterval
	slotsPerDay := int64(86400) / n.Config.SlotInterval

	nextBlockIndex := n.Blockchain.GetLatestBlock().Index + 1

	// Clone and purge CommitStore for mempool validation snapshot
	commitSnap := blockchain.NewCommitOverlay(n.CommitStore.ExportPending(), nextBlockIndex)
	commitSnap.PurgeExpired(nextBlockIndex)

	n.StakeMutex.Lock()
	stakeClone := n.StakeMap.Clone()
	queueClone := n.UnstakeQueue.Clone()
	evidenceCopy := copyStringBoolMap(n.SlashedEvidence)
	n.StakeMutex.Unlock()

	if !blockchain.ValidateTransactions(pendingList, n.BalanceLedger, n.IndexManager, commitSnap,
		currentSlot, slotsPerDay, false, nil, nextBlockIndex,
		stakeClone, queueClone, evidenceCopy) {
		log.Printf("AddTransaction: rejected TID %d — validation failed", tx.TID)
		return
	}

	n.TransactionPool[tx.TID] = tx
}

// HandleINV processes inventory message and requests missing blocks
func (n *Node) HandleINV(sender string) {
	n.BcMutex.Lock()
	defer n.BcMutex.Unlock()

	localHeight := n.Blockchain.GetLatestBlock().Index

	getBlockMsg := map[string]int{
		"height": int(localHeight),
	}
	n.P2PNetwork.DirectMessage(MsgGetBlock, getBlockMsg, sender)
	log.Printf("[INV] %s requested blocks from height %d\n", n.Address, localHeight)
}

// HandleGetData responds with transactions in the mempool
func (n *Node) HandleGetData(sender string) {
	n.TxMutex.Lock()
	defer n.TxMutex.Unlock()

	for _, tx := range n.TransactionPool {
		n.P2PNetwork.DirectMessage(MsgTransaction, tx, sender)
	}
	log.Printf("[GETDATA] %s sent mempool transactions to %s\n", n.Address, sender)
}

// HandleGetBlock responds with full blockchain blocks starting from a given height
func (n *Node) HandleGetBlock(sender string) {
	n.BcMutex.Lock()
	defer n.BcMutex.Unlock()

	// Respond with blocks newer than peer's height
	start := n.Blockchain.GetLatestBlock().Index - 5 // sending last 5 blocks for now
	if start < 0 {
		start = 0
	}

	blocks := n.Blockchain.GetBlocksFrom(int(start))
	for _, b := range blocks {
		n.P2PNetwork.DirectMessage(MsgBlock, b, sender)
	}
	log.Printf("[GETBLOCK] %s sent recent blocks to %s\n", n.Address, sender)
}

// HandleMerkleRequest sends a proper Merkle proof for a domain query
func (n *Node) HandleMerkleRequest(sender string, domain string) {
	n.TxMutex.Lock()
	defer n.TxMutex.Unlock()

	// Look up transaction location via IndexManager
	loc := n.IndexManager.GetTxLocation(domain)
	if loc == nil {
		log.Printf("[MERKLE] Domain %s not found in index\n", domain)
		return
	}

	n.BcMutex.Lock()
	block := n.Blockchain.GetBlockByIndex(loc.BlockIndex)
	n.BcMutex.Unlock()

	if block == nil {
		log.Printf("[MERKLE] Block %d not found\n", loc.BlockIndex)
		return
	}

	proof := block.GenerateMerkleProof(loc.TxIndex)
	if proof == nil {
		log.Printf("[MERKLE] Failed to generate proof for tx at index %d\n", loc.TxIndex)
		return
	}

	n.P2PNetwork.DirectMessage(MsgMerkleProof, proof, sender)
	log.Printf("[MERKLE] %s sent Merkle proof for %s to %s\n", n.Address, domain, sender)
}

// DNSQueryMsg is sent by light nodes to request domain resolution with proof
type DNSQueryMsg struct {
	DomainName string
}

// DNSProofResponse contains the answer + cryptographic proof for light node verification
type DNSProofResponse struct {
	DomainName  string
	Records     []blockchain.Record
	Transaction blockchain.Transaction
	Proof       blockchain.MerkleProof
	BlockHeader blockchain.BlockHeader
}

// HandleDNSQuery handles a DNS query from a light node (full node only)
func (n *Node) HandleDNSQuery(sender string, query DNSQueryMsg) {
	if !n.IsFullNode {
		return
	}

	n.TxMutex.Lock()
	tx := n.IndexManager.GetDomain(query.DomainName)
	if tx == nil {
		n.TxMutex.Unlock()
		log.Println("[DNS_QUERY] Domain not found:", query.DomainName)
		return
	}

	// only serve proofs for active domains
	slotsPerDay := int64(86400) / n.Config.SlotInterval
	currentSlot := (time.Now().Unix() - n.Config.InitialTimestamp) / n.Config.SlotInterval
	phase := blockchain.GetDomainPhase(currentSlot, tx.ExpirySlot, slotsPerDay)
	if phase != "active" {
		n.TxMutex.Unlock()
		log.Printf("[DNS_QUERY] Domain %s is in %s phase, not serving proof\n", query.DomainName, phase)
		return
	}// Waits for the block header, validates the Merkle proof, and checks K-confirmations.

	loc := n.IndexManager.GetTxLocation(query.DomainName)
	n.TxMutex.Unlock()

	if loc == nil {
		return
	}// Waits for the block header, validates the Merkle proof, and checks K-confirmations.

	n.BcMutex.Lock()
	block := n.Blockchain.GetBlockByIndex(loc.BlockIndex)
	n.BcMutex.Unlock()

	if block == nil {
		return
	}

	proof := block.GenerateMerkleProof(loc.TxIndex)
	if proof == nil {
		return
	}

	response := DNSProofResponse{
		DomainName:  query.DomainName,
		Records:     tx.Records,
		Transaction: *tx,
		Proof:       *proof,
		BlockHeader: block.Header(),
	}
	n.P2PNetwork.DirectMessage(MsgDNSProof, response, sender)
	log.Printf("[DNS_QUERY] %s sent proof for %s to %s\n", n.Address, query.DomainName, sender)
}

// HandleDNSProof verifies a DNS proof received from a full node (light node only)
func (n *Node) HandleDNSProof(response DNSProofResponse) {
	if n.IsFullNode {
		return
	}

	// Optimistic waiting: header might arrive slightly after proof
	header := n.waitForHeader(response.BlockHeader.Index, 2*time.Second)
	if header == nil {
		log.Println("[DNS_PROOF] Header not received after timeout, block:", response.BlockHeader.Index)
		return
	}

	if !bytes.Equal(header.Hash, response.BlockHeader.Hash) {
		log.Println("[DNS_PROOF] Block header hash mismatch — possible attack!")
		return
	}

	// Verify Merkle proof against local header's root
	response.Proof.MerkleRoot = header.MerkleRoot
	if !blockchain.VerifyMerkleProof(&response.Proof) {
		log.Println("[DNS_PROOF] Merkle proof verification failed — data tampered!")
		return
	}

	// Verified! Cache the first A record (or empty if none)
	firstA := ""
	for _, r := range response.Records {
		if r.Type == "A" {
			firstA = r.Value
			break
		}
	}
	log.Printf("[DNS_PROOF] Verified: %s → %s (block #%d)\n",
		response.DomainName, firstA, response.BlockHeader.Index)
	// Cache any A records from the proof under queryType "A"
	aRecords := filterByType(response.Records, "A")
	if len(aRecords) > 0 {
		SetToCache(response.DomainName, "A", aRecords)
	}
}

// waitForHeader polls for a block header with a timeout (handles network lag)
func (n *Node) waitForHeader(index int64, timeout time.Duration) *blockchain.BlockHeader {
	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond

	for {
		if int(index) < len(n.HeaderChain) {
			return &n.HeaderChain[index]
		}

		if time.Now().After(deadline) {
			return nil
		}

		time.Sleep(pollInterval)
	}
}

// VerifyNXDOMAIN confirms domain absence via TOFU consensus across healthy full nodes.
func (n *Node) VerifyNXDOMAIN(domain string) bool {
	if n.ConnectionPool == nil {
		return false
	}
	pool, ok := n.ConnectionPool.(*gateway.ConnectionPool)
	if !ok {
		return false
	}
	if pool.GetHealthyCount() < 2 {
		return false
	}
	_, err := pool.QueryWithFailover(domain, 0)
	return err != nil
}

// HandleDNSProofGRPC verifies // Waits for the block header, validates the Merkle proof, and checks K-confirmations.a DNS proof delivered over gRPC by the gateway layer.
func (n *Node) HandleDNSProofGRPC(resp *pb.DomainQueryResponse) bool {
	if resp == nil || resp.BlockHeader == nil || resp.Proof == nil {
		return false
	}

	header := n.waitForHeader(resp.BlockHeader.Index, 2*time.Second)
	if header == nil {
		log.Printf("[SPV] header %d not received within timeout", resp.BlockHeader.Index)
		return false
	}

	if !bytes.Equal(header.Hash, resp.BlockHeader.Hash) {
		log.Println("[SPV] block header hash mismatch")
		return false
	}

	proof := blockchain.MerkleProof{
		MerkleRoot: header.MerkleRoot,
		TxHash:     resp.Proof.TxHash,
		ProofPath:  resp.Proof.ProofPath,
		Directions: resp.Proof.Directions,
	}
	if !blockchain.VerifyMerkleProof(&proof) {
		log.Println("[SPV] Merkle proof verification failed")
		return false
	}

	// K-confirmation depth check
	n.BcMutex.Lock()
	tip := n.Blockchain.GetLatestBlock().Index
	n.BcMutex.Unlock()
	const kConfirmations = 6
	if tip-resp.BlockHeader.Index < kConfirmations {
		log.Printf("[SPV] insufficient confirmations: have %d, need %d", tip-resp.BlockHeader.Index, kConfirmations)
		return false
	}

	return true
}
