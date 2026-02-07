package network

import (
	"bytes"
	"log"

	"github.com/bleasey/bdns/internal/blockchain"
)

func (n *Node) AddBlock(block *blockchain.Block) {
	epoch := (block.Timestamp - n.Config.InitialTimestamp) / (n.Config.SlotInterval * n.Config.SlotsPerEpoch)
	slotLeader := n.GetSlotLeader(epoch)

	// Verify received block
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

// HandleMerkleRequest sends Merkle proof path for a record
func (n *Node) HandleMerkleRequest(sender string) {
	n.BcMutex.Lock()
	defer n.BcMutex.Unlock()

	// Simplified: just sending full block instead of real Merkle path
	// Ideally: compute Merkle root and proof path
	latest := n.Blockchain.GetLatestBlock()
	if latest != nil {
		n.P2PNetwork.DirectMessage(MsgBlock, latest, sender)
		log.Printf("[MERKLE] %s sent block with Merkle data to %s\n", n.Address, sender)
	}
}
