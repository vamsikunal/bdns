package network

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/consensus"
	"github.com/bleasey/bdns/internal/index"
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

		// Convert StakeData from uint64 map to int map for consensus compatibility
		stakeInt := make(map[string]int, len(latestBlock.StakeData))
		for k, v := range latestBlock.StakeData {
			stakeInt[k] = int(v)
		}
		slotLeader = consensus.GetSlotLeaderUtil(n.RegistryKeys, stakeInt, n.EpochRandoms[epoch])
	}

	n.SlotLeaders[epoch] = slotLeader
	return slotLeader
}

func (n *Node) CreateBlockIfLeader(ctx context.Context) {
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
	if err := n.BroadcastCommitment(1); err != nil {
		log.Printf("BroadcastCommitment epoch 1 failed: %v", err)
	}
	time.Sleep(time.Duration(n.Config.SlotInterval*n.Config.SlotsPerEpoch) * time.Second) // wait till end of epoch

	// Initialize loop variables
	slot := int64(n.Config.SlotsPerEpoch - 1)
	epoch := int64(0)
	blockTxLimit := 10

	// Ticker loop for slots
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		slot++
		newEpoch := slot / n.Config.SlotsPerEpoch

		// Update epoch and leader only when the epoch changes
		if newEpoch != epoch {
			epoch = newEpoch
			currSlotLeader = n.GetSlotLeader(epoch)
			// Slot 0 of new epoch: broadcast commitment
			if slot%n.Config.SlotsPerEpoch == 0 {
				if err := n.BroadcastCommitment(epoch + 1); err != nil {
					log.Printf("BroadcastCommitment epoch %d failed: %v", epoch+1, err)
				}
			}
			// Slot 1 of new epoch: broadcast reveal
			if slot%n.Config.SlotsPerEpoch == 1 {
				if err := n.BroadcastReveal(epoch); err != nil {
					log.Printf("BroadcastReveal epoch %d failed: %v", epoch, err)
				}
				n.PruneDRGEpochState(epoch)
			}
		}

		// Only the current slot leader should produce a block
		if !bytes.Equal(currSlotLeader, n.KeyPair.PublicKey) {
			continue
		}

		// Create block from transactions
		fmt.Printf("Node %s is the slot leader, creating a block...\n", n.Address)

		transactions := n.ChooseTxFromPool(blockTxLimit)

		// Create CommitOverlay from the live store and purge expired commits
		nextIdx := n.Blockchain.GetLatestBlock().Index + 1
		commitOverlay := blockchain.NewCommitOverlay(n.CommitStore.ExportPending(), nextIdx)
		commitOverlay.PurgeExpired(nextIdx)

		// Add auto-revocation transactions for expired domains
		// Prepend so COMMIT/REVEAL txs are processed before auto-REVOKE in same block
		autoRevocations := n.GenerateAutoRevocations(slot, transactions)
		transactions = append(autoRevocations, transactions...)

		if len(transactions) == 0 {
			fmt.Println("No transactions to add. Skipping block creation.")
			continue
		}

		fmt.Println("Transactions in block:", len(transactions), "(Auto-revocations:", len(autoRevocations), ")")

		n.BcMutex.Lock()
		latestBlock := n.Blockchain.GetLatestBlock()

		if latestBlock.SlotNumber >= slot {
			n.BcMutex.Unlock()
			fmt.Printf("[LEADER] Slot %d already committed (latest slot: %d), skipping\n", slot, latestBlock.SlotNumber)
			n.TxMutex.Lock()
			for i := range transactions {
				n.TransactionPool[transactions[i].TID] = &transactions[i]
			}
			n.TxMutex.Unlock()
			continue
		}
		n.BcMutex.Unlock()

		// Phase A: nonce + fee on staging clone
		n.TxMutex.Lock()
		slotsPerDay := int64(86400) / n.Config.SlotInterval

		staging := n.BalanceLedger.Clone()
		for _, tx := range transactions {
			blockchain.ApplyLedgerMutations(tx, staging)
		}
		totalFees := uint64(0)
		for _, tx := range transactions {
			totalFees += tx.Fee
		}
		if totalFees > 0 {
			staging.Credit(hex.EncodeToString(n.KeyPair.PublicKey), totalFees)
		}

		// Phase B: domain mutations on overlay (pass CommitOverlay for COMMIT/REVEAL)
		nextBlockIndex := latestBlock.Index + 1
		imOverlay := index.NewIndexOverlay(n.IndexManager)
		for i, tx := range transactions {
			blockchain.ApplyDomainMutations(tx, staging, imOverlay, commitOverlay, slot, nextBlockIndex, i, slotsPerDay)
		}

		balanceLedgerHash := staging.Hash()
		indexHash := imOverlay.GetIndexHash()
		commitStoreHash := commitOverlay.Hash()
		n.TxMutex.Unlock()

		// Seal block
		newBlock := blockchain.NewBlock(latestBlock.Index+1, slot, currSlotLeader,
			indexHash, balanceLedgerHash, commitStoreHash, transactions, latestBlock.Hash,
			nil, nil, latestBlock.StakeData, &n.KeyPair.PrivateKey)

		// Phase C: commit overlays to real state
		n.BalanceLedger = staging
		imOverlay.Commit()
		commitOverlay.Commit(n.CommitStore)

		// Persist block + BoltDB spent markers
		n.BcMutex.Lock()
		n.Blockchain.AddBlock(newBlock)
		for _, tx := range transactions {
			if tx.RedeemsTxID != 0 && (tx.Type == blockchain.UPDATE ||
				tx.Type == blockchain.RENEW || tx.Type == blockchain.REVOKE) {
				n.Blockchain.MarkAsSpent(tx.RedeemsTxID)
			}
		}
		n.BcMutex.Unlock()

		n.P2PNetwork.BroadcastMessage(MsgBlock, *newBlock, nil)
		fmt.Print("Block ", newBlock.Index, " created and broadcasted by node ", n.Address, "\n\n")
	}
}

// GenerateAutoRevocations creates REVOKE transactions now for domains past their purge slot
func (n *Node) GenerateAutoRevocations(currentSlot int64, pendingTxs []blockchain.Transaction) []blockchain.Transaction {
	purgeableDomains := n.IndexManager.GetPurgeableDomains(currentSlot)
	if len(purgeableDomains) == 0 {
		return nil
	}

	// Build a set of domains being renewed in this block's pending transactions.
	renewedDomains := make(map[string]bool)
	for _, tx := range pendingTxs {
		if tx.Type == blockchain.RENEW {
			renewedDomains[tx.DomainName] = true
		}
	}

	// Filter out renewed domains BEFORE sorting — no point sorting entries we'll discard
	filtered := make([]*blockchain.Transaction, 0, len(purgeableDomains))
	for _, tx := range purgeableDomains {
		if renewedDomains[tx.DomainName] {
			log.Println("Skipping auto-revoke for", tx.DomainName, "— pending RENEW in pool")
			continue
		}
		filtered = append(filtered, tx)
	}

	if len(filtered) == 0 {
		return nil
	}

	// Sort alphabetically by domain name for deterministic ordering
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].DomainName < filtered[j].DomainName
	})

	revocations := make([]blockchain.Transaction, 0, len(filtered))
	for _, tx := range filtered {
		revokeTx := blockchain.Transaction{
			TID:         tx.TID,
			Type:        blockchain.REVOKE,
			Timestamp:   0,
			DomainName:  tx.DomainName,
			CacheTTL:    0,
			ExpirySlot:  tx.ExpirySlot,
			RedeemsTxID: tx.TID,
			OwnerKey:    nil,
			Signature:   nil,
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
