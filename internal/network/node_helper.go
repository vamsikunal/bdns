package network

import (
	"bytes"
	"log"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
)

func (n *Node) AddBlock(block *blockchain.Block) {
	epoch := (block.Timestamp - n.Config.InitialTimestamp) / (n.Config.SlotInterval * n.Config.SlotsPerEpoch)
	slotLeader := n.GetSlotLeader(epoch)

	// Verify received block (pass IndexManager for expiration validation)
	if (block.Index == 0 && !blockchain.ValidateGenesisBlock(block, n.RegistryKeys, slotLeader)) ||
		(block.Index != 0 && !blockchain.ValidateBlock(block, n.Blockchain.GetLatestBlock(), slotLeader, n.IndexManager)) {
		log.Println("Invalid block received at ", n.Address)
		return
	}

	// Update index tree
	n.TxMutex.Lock()
	for i := range block.Transactions {
		tx := &block.Transactions[i]
		switch tx.Type {
		case blockchain.REGISTER:
			n.IndexManager.Add(tx.DomainName, tx, block.Index, i)

		case blockchain.UPDATE:
			// Remove old tx from expiry index before updating
			if oldTx := n.IndexManager.GetIP(tx.DomainName); oldTx != nil {
				n.IndexManager.RemoveFromExpiryIndex(oldTx)
			}
			n.IndexManager.Update(tx.DomainName, tx)

		case blockchain.REVOKE:
			// Remove from expiry index before removing from tree
			if oldTx := n.IndexManager.GetIP(tx.DomainName); oldTx != nil {
				n.IndexManager.RemoveFromExpiryIndex(oldTx)
			}
			n.IndexManager.Remove(tx.DomainName)
		}
	}
	blockchain.RemoveTxsFromPool(block.Transactions, n.TransactionPool)
	n.TxMutex.Unlock()

	// Validate IndexHash after applying transactions
	expectedHash := n.IndexManager.GetIndexHash()
	if !bytes.Equal(block.IndexHash, expectedHash) {
		log.Println("IndexHash mismatch - rejecting block and rolling back at", n.Address)
		// ROLLBACK: Reverse the transactions we just applied
		n.TxMutex.Lock()
		n.rollbackTransactions(block.Transactions)
		n.TxMutex.Unlock()
		return
	}

	// ATOMIC: Lock mutex for BOTH AddBlock AND MarkAsSpent
	n.BcMutex.Lock()
	n.Blockchain.AddBlock(block)

	// Mark spent TxIDs
	for _, tx := range block.Transactions {
		if tx.Type == blockchain.UPDATE ||
			(tx.Type == blockchain.REVOKE && tx.RedeemsTxID != 0) {
			n.Blockchain.MarkAsSpent(tx.RedeemsTxID)
		}
	}
	n.BcMutex.Unlock()
}

// rollbackTransactions reverses applied transactions on validation failure
func (n *Node) rollbackTransactions(transactions []blockchain.Transaction) {
	// Reverse iterate and undo each operation
	for i := len(transactions) - 1; i >= 0; i-- {
		tx := transactions[i]
		switch tx.Type {
		case blockchain.REGISTER:
			n.IndexManager.Remove(tx.DomainName)
		case blockchain.UPDATE:
			log.Println("Warning: UPDATE rollback - domain state may be inconsistent:", tx.DomainName)
		case blockchain.REVOKE:
			log.Println("Warning: REVOKE rollback - cannot restore domain:", tx.DomainName)
		}
	}
}

func (n *Node) AddTransaction(tx *blockchain.Transaction) {
	n.TxMutex.Lock()
	defer n.TxMutex.Unlock()
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
	IP          string
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
	tx := n.IndexManager.GetIP(query.DomainName)
	if tx == nil {
		n.TxMutex.Unlock()
		log.Println("[DNS_QUERY] Domain not found:", query.DomainName)
		return
	}

	loc := n.IndexManager.GetTxLocation(query.DomainName)
	n.TxMutex.Unlock()

	if loc == nil {
		return
	}

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
		IP:          tx.IP,
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

	// Verified! Cache the result
	log.Printf("[DNS_PROOF] Verified: %s → %s (block #%d)\n",
		response.DomainName, response.IP, response.BlockHeader.Index)
	SetToCache(response.DomainName, response.IP)
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
