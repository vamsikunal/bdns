package network

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/consensus"
)

func (n *Node) GetSlotLeader(epoch int64) []byte {
	n.SlotMutex.Lock()
	defer n.SlotMutex.Unlock()

	slotLeader, exists := n.SlotLeaders[epoch]
	if exists {
		return slotLeader
	}

	// Assuming map miss only happens for current epoch
	if epoch == 0 {
		slotLeader = consensus.GetSlotLeaderUtil(n.RegistryKeys, nil, n.EpochRandoms[epoch])
	} else {
		n.BcMutex.Lock()
		latestBlock := n.Blockchain.GetLatestBlock()
		n.BcMutex.Unlock()

		slotLeader = consensus.GetSlotLeaderUtil(n.RegistryKeys, latestBlock.StakeData, n.EpochRandoms[epoch])
	}

	n.SlotLeaders[epoch] = slotLeader
	return slotLeader
}

func (n *Node) CreateBlockIfLeader() {
	ticker := time.NewTicker(time.Duration(n.Config.SlotInterval) * time.Second)
	defer ticker.Stop()

	// GENESIS BLOCK creation
	currSlotLeader := n.RegistryKeys[0] // Default genesis slot leader

	if bytes.Equal(currSlotLeader, n.KeyPair.PublicKey) {
		fmt.Println("Node", n.Address, "is the slot leader for the genesis block")

		seedBytes := []byte(fmt.Sprintf("%f", n.Config.Seed))
		genesisBlock := blockchain.NewGenesisBlock(currSlotLeader, &n.KeyPair.PrivateKey, n.RegistryKeys, seedBytes)

		n.BcMutex.Lock()
		n.Blockchain.AddBlock(genesisBlock)
		n.BcMutex.Unlock()

		n.P2PNetwork.BroadcastMessage(MsgBlock, *genesisBlock, nil)
		fmt.Print("Genesis block created and broadcasted by node ", n.Address, "\n\n")
	}
	n.BroadcastRandomNumber(1)                                                            // Broadcast nums for the fist epoch
	time.Sleep(time.Duration(n.Config.SlotInterval*n.Config.SlotsPerEpoch) * time.Second) // wait till end of epoch

	// Initialize loop variables
	slot := int64(n.Config.SlotsPerEpoch - 1)
	epoch := int64(0)
	blockTxLimit := 10

	// Ticker loop for slots
	for range ticker.C {
		slot++
		newEpoch := slot / n.Config.SlotsPerEpoch

		// Update epoch and leader only when the epoch changes
		if newEpoch != epoch {
			epoch = newEpoch
			currSlotLeader = n.GetSlotLeader(epoch)
			n.BroadcastRandomNumber(epoch + 1) // Send rand nums for next epoch
		}

		// Only the current slot leader should produce a block
		if !bytes.Equal(currSlotLeader, n.KeyPair.PublicKey) {
			continue
		}

		// Create block from transactions
		fmt.Printf("Node %s is the slot leader, creating a block...\n", n.Address)

		transactions := n.ChooseTxFromPool(blockTxLimit)

		// Add auto-revocation transactions for expired domains
		autoRevocations := n.GenerateAutoRevocations(slot)
		transactions = append(transactions, autoRevocations...)

		if len(transactions) == 0 {
			fmt.Println("No transactions to add. Skipping block creation.")
			continue
		}

		fmt.Println("Transactions in block:", len(transactions), "(Auto-revocations:", len(autoRevocations), ")")

		n.BcMutex.Lock()
		latestBlock := n.Blockchain.GetLatestBlock()
		n.BcMutex.Unlock()

		// Apply transactions to index BEFORE creating block (for IndexHash)
		n.TxMutex.Lock()
		for i := range transactions {
			tx := &transactions[i]
			switch tx.Type {
			case blockchain.REGISTER:
				n.IndexManager.Add(tx.DomainName, tx, latestBlock.Index+1, i)
			case blockchain.UPDATE:
				if oldTx := n.IndexManager.GetIP(tx.DomainName); oldTx != nil {
					n.IndexManager.RemoveFromExpiryIndex(oldTx)
				}
				n.IndexManager.Update(tx.DomainName, tx)
			case blockchain.REVOKE:
				if oldTx := n.IndexManager.GetIP(tx.DomainName); oldTx != nil {
					n.IndexManager.RemoveFromExpiryIndex(oldTx)
				}
				n.IndexManager.Remove(tx.DomainName)
			}
		}
		n.TxMutex.Unlock()

		// Compute IndexHash AFTER applying transactions
		indexHash := n.IndexManager.GetIndexHash()

		// Create block WITH IndexHash
		newBlock := blockchain.NewBlock(latestBlock.Index+1, slot, currSlotLeader, indexHash, transactions, latestBlock.Hash, latestBlock.StakeData, &n.KeyPair.PrivateKey)

		// ATOMIC: Add block AND mark spent under same lock
		n.BcMutex.Lock()
		n.Blockchain.AddBlock(newBlock)

		// Mark spent TxIDs (inside same lock for atomicity)
		for _, tx := range transactions {
			if tx.Type == blockchain.UPDATE ||
				(tx.Type == blockchain.REVOKE && tx.RedeemsTxID != 0) {
				n.Blockchain.MarkAsSpent(tx.RedeemsTxID)
			}
		}
		n.BcMutex.Unlock()

		// Broadcast to others (outside lock)
		n.P2PNetwork.BroadcastMessage(MsgBlock, *newBlock, nil)
		fmt.Print("Block ", newBlock.Index, " created and broadcasted by node ", n.Address, "\n\n")
	}
}

// GenerateAutoRevocations creates REVOKE transactions for expired domains
func (n *Node) GenerateAutoRevocations(currentSlot int64) []blockchain.Transaction {
	expiredDomains := n.IndexManager.GetExpiredDomains(currentSlot)
	if len(expiredDomains) == 0 {
		return nil
	}

	// Sort alphabetically by domain name for deterministic ordering
	sort.Slice(expiredDomains, func(i, j int) bool {
		return expiredDomains[i].DomainName < expiredDomains[j].DomainName
	})

	revocations := make([]blockchain.Transaction, 0, len(expiredDomains))
	for _, tx := range expiredDomains {
		revokeTx := blockchain.Transaction{
			TID:         tx.TID, 
			Type:        blockchain.REVOKE,
			Timestamp:   0,
			DomainName:  tx.DomainName,
			IP:          "",
			CacheTTL:    0,
			ExpirySlot:  tx.ExpirySlot,
			RedeemsTxID: tx.TID, // Redeems the original registration
			OwnerKey:    nil,    // No owner key - system transaction
			Signature:   nil,    // No signature - validated by expiry check
		}
		revocations = append(revocations, revokeTx)
	}

	return revocations
}

func (n *Node) ChooseTxFromPool(limit int) []blockchain.Transaction {
	n.TxMutex.Lock()
	defer n.TxMutex.Unlock()

	if len(n.TransactionPool) == 0 {
		return nil
	}

	transactions := make([]blockchain.Transaction, 0, limit)
	for _, tx := range n.TransactionPool {
		if len(transactions) >= limit {
			break
		}
		transactions = append(transactions, *tx)
		delete(n.TransactionPool, tx.TID)
	}

	return transactions
}
