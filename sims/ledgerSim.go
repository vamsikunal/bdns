package sims

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/network"
)

// LedgerSim: to check trustless ledger economic boundaries

// Network: 6 nodes, 5s slots, 2 slots/epoch.
func LedgerSim() {
	const (
		numNodes      = 6
		slotInterval  = 5
		slotsPerEpoch = 2
		seed          = 42
		slotsPerDay   = int64(86400 / slotInterval)
		fee           = uint64(1)
	)

	nodes := network.InitializeP2PNodes(numNodes, slotInterval, slotsPerEpoch, seed)
	metrics := &ledgerMetrics{}

	fmt.Println("=== Ledger Sim: waiting for genesis block ===")
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch) * time.Second)

	// STAKE phase: Each node STAKEs coins to become eligible for leader election
	fmt.Println("[LedgerSim] Issuing STAKE transactions...")
	for i, node := range nodes {
		pubKeyHex := hex.EncodeToString(node.KeyPair.PublicKey)
		nonce := node.BalanceLedger.GetNonce(pubKeyHex)

		stakeTx := blockchain.NewStakeTransaction(10000,
			node.KeyPair.PublicKey, &node.KeyPair.PrivateKey,
			1, nonce, node.TransactionPool)

		node.BroadcastTransaction(*stakeTx)
		fmt.Printf("[STAKE] node%d staked 10000 coins\n", i+1)
		time.Sleep(200 * time.Millisecond)
	}

	fmt.Println("[LedgerSim] Waiting for STAKEs to be mined...")
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch*3) * time.Second)

	// Node aliases
	nodeA := nodes[0] // TrustedRegistry — registers, lists, delists
	nodeB := nodes[2] // TrustedRegistry — receives FUND + BUY
	nodeC := nodes[3] // TrustedRegistry — receives TRANSFER

	pubA := hex.EncodeToString(nodeA.KeyPair.PublicKey)
	pubB := hex.EncodeToString(nodeB.KeyPair.PublicKey)

	// Helpers
	getNonce := func(node *network.Node) uint64 {
		return node.BalanceLedger.GetNonce(hex.EncodeToString(node.KeyPair.PublicKey))
	}
	getBal := func(node *network.Node, pubHex string) uint64 {
		return node.BalanceLedger.GetBalance(pubHex)
	}

	balABefore := getBal(nodeA, pubA)
	fmt.Printf("[INFO] NodeA starting balance: %d\n", balABefore)

	// Register the primary test domain via COMMIT→REVEAL
	fmt.Println("\n=== Ledger Sim: Fee & Nonce Tests ===")
	nonceA := getNonce(nodeA)
	primaryRecords := []blockchain.Record{{Type: "A", Value: "10.0.0.1", Priority: 0}}
	primarySalt := []byte("ledger-test-salt-1")
	commitPrimary := blockchain.NewCommitTransaction("ledger-test.bdns",
		primarySalt, nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	nodeA.BroadcastTransaction(*commitPrimary)
	fmt.Printf("[COMMIT] ledger-test.bdns, fee=%d nonce=%d\n", fee, nonceA)

	waitBlocks(slotInterval, slotsPerEpoch, int(blockchain.CommitMinDelay)+1)

	nonceA = getNonce(nodeA)
	nextBlock := nodeA.Blockchain.GetLatestBlock().Index + 1
	txReg := blockchain.NewRevealTransaction("ledger-test.bdns", primarySalt, primaryRecords,
		3600, nextBlock, slotsPerDay, commitPrimary.TID,
		nodeA.KeyPair.PublicKey, nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	nodeA.BroadcastTransaction(*txReg)
	fmt.Printf("[REVEAL] ledger-test.bdns — A record, fee=%d nonce=%d\n", fee, nonceA)

	waitBlocks(slotInterval, slotsPerEpoch, 2)

	balAAfter := getBal(nodeA, pubA)
	if balAAfter < balABefore-10 {
		metrics.record(false)
		fmt.Printf("FAIL  [REGISTER fee] — balance dropped too much: %d→%d\n", balABefore, balAAfter)
	} else {
		metrics.record(true)
		fmt.Printf("PASS  [REGISTER fee] — %d→%d\n", balABefore, balAAfter)
	}

	// Check nonce advanced
	nonceACommitted := getNonce(nodeA)
	if nonceACommitted > nonceA {
		metrics.record(true)
		fmt.Printf("PASS  [nonce increment] — %d→%d\n", nonceA, nonceACommitted)
	} else {
		metrics.record(false)
		fmt.Printf("FAIL  [nonce increment] — still %d\n", nonceACommitted)
	}

	// Verify domain actually indexed
	qn := bestQueryNode(nodes, []string{"ledger-test.bdns"})
	currentSlot := (time.Now().Unix() - qn.Config.InitialTimestamp) / qn.Config.SlotInterval
	recs, err := network.ResolveDomain("ledger-test.bdns", "A", qn, currentSlot, slotsPerDay)
	if err == nil && len(recs) > 0 && recs[0].Value == "10.0.0.1" {
		metrics.record(true)
		fmt.Printf("PASS  [A ledger-test.bdns] — %s\n", recs[0].Value)
	} else {
		metrics.record(false)
		fmt.Printf("FAIL  [A ledger-test.bdns] — err=%v recs=%v\n", err, recs)
	}

	// Sequential nonces — register two more domains back-to-back
	nonceA = getNonce(nodeA)
	seq1Salt := []byte("seq1-salt")
	seq1Commit := blockchain.NewCommitTransaction("seq1.bdns",
		seq1Salt, nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	nodeA.BroadcastTransaction(*seq1Commit)
	nonceA++

	seq2Salt := []byte("seq2-salt")
	seq2Commit := blockchain.NewCommitTransaction("seq2.bdns",
		seq2Salt, nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	nodeA.BroadcastTransaction(*seq2Commit)
	fmt.Printf("[COMMIT] seq1.bdns nonce=%d, seq2.bdns nonce=%d\n", getNonce(nodeA)-2, getNonce(nodeA)-1)

	waitBlocks(slotInterval, slotsPerEpoch, int(blockchain.CommitMinDelay)+2)

	nonceA = getNonce(nodeA)
	nextBlock = nodeA.Blockchain.GetLatestBlock().Index + 1
	tx2a := blockchain.NewRevealTransaction("seq1.bdns", seq1Salt,
		[]blockchain.Record{{Type: "A", Value: "10.0.0.2", Priority: 0}},
		3600, nextBlock, slotsPerDay, seq1Commit.TID,
		nodeA.KeyPair.PublicKey, nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	nodeA.BroadcastTransaction(*tx2a)
	nonceA++

	nextBlock = nodeA.Blockchain.GetLatestBlock().Index + 1
	tx2b := blockchain.NewRevealTransaction("seq2.bdns", seq2Salt,
		[]blockchain.Record{{Type: "A", Value: "10.0.0.3", Priority: 0}},
		3600, nextBlock, slotsPerDay, seq2Commit.TID,
		nodeA.KeyPair.PublicKey, nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	nodeA.BroadcastTransaction(*tx2b)
	fmt.Printf("[REVEAL] seq1.bdns + seq2.bdns\n")

	waitBlocks(slotInterval, slotsPerEpoch, 2)

	qn = bestQueryNode(nodes, []string{"seq1.bdns", "seq2.bdns"})
	currentSlot = (time.Now().Unix() - qn.Config.InitialTimestamp) / qn.Config.SlotInterval
	r1, e1 := network.ResolveDomain("seq1.bdns", "A", qn, currentSlot, slotsPerDay)
	r2, e2 := network.ResolveDomain("seq2.bdns", "A", qn, currentSlot, slotsPerDay)
	if e1 == nil && e2 == nil && len(r1) > 0 && len(r2) > 0 {
		metrics.record(true)
		fmt.Println("PASS  [sequential nonces] — seq1 + seq2 both resolved")
	} else {
		metrics.record(false)
		fmt.Printf("FAIL  [sequential nonces] — seq1=%v seq2=%v\n", e1, e2)
	}

	// Nonce gap — skip 5 nonces, should be rejected
	nonceA = getNonce(nodeA)
	txGap := blockchain.NewTransaction(blockchain.REGISTER, "gap.bdns",
		[]blockchain.Record{{Type: "A", Value: "10.0.0.99", Priority: 0}},
		3600, 0, slotsPerDay, 0,
		nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey, nodeA.TransactionPool, fee, nonceA+5) // skip 5 nonces
	nodeA.BroadcastTransaction(*txGap)
	fmt.Printf("[REGISTER] gap.bdns with nonce=%d (current=%d) — expect rejection\n", nonceA+5, nonceA)

	waitBlocks(slotInterval, slotsPerEpoch, 1)

	qn = bestQueryNode(nodes, []string{"gap.bdns"})
	currentSlot = (time.Now().Unix() - qn.Config.InitialTimestamp) / qn.Config.SlotInterval
	_, errGap := network.ResolveDomain("gap.bdns", "A", qn, currentSlot, slotsPerDay)
	if errGap != nil {
		metrics.record(true)
		fmt.Println("PASS  [nonce gap] — domain not indexed (correct)")
	} else {
		metrics.record(false)
		fmt.Println("FAIL  [nonce gap] — domain was indexed despite gap!")
	}

	// FUND nodeB
	fmt.Println("\n=== Ledger Sim: FUND Tests ===")
	nonceA = getNonce(nodeA)
	fundAmount := uint64(5000)
	balBBefore := getBal(nodeA, pubB)

	txFund := blockchain.NewFundTransaction(
		nodeB.KeyPair.PublicKey, fundAmount,
		nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	nodeA.BroadcastTransaction(*txFund)
	fmt.Printf("[FUND] %d B-Coins nodeA→nodeB, fee=%d nonce=%d\n", fundAmount, fee, nonceA)

	waitBlocks(slotInterval, slotsPerEpoch, 2)

	balBAfter := getBal(nodeA, pubB)
	if balBAfter >= balBBefore+fundAmount {
		metrics.record(true)
		fmt.Printf("PASS  [FUND credited] — nodeB %d→%d\n", balBBefore, balBAfter)
	} else {
		metrics.record(false)
		fmt.Printf("FAIL  [FUND credited] — nodeB %d→%d (expected +%d)\n", balBBefore, balBAfter, fundAmount)
	}

	// LIST domain for sale
	fmt.Println("\n=== Ledger Sim: Marketplace Tests ===")
	nonceA = getNonce(nodeA)
	qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
	qn.TxMutex.Lock()
	regTx := qn.IndexManager.GetDomain("ledger-test.bdns")
	redeemTID := 0
	if regTx != nil {
		redeemTID = regTx.TID
	}
	qn.TxMutex.Unlock()

	if redeemTID == 0 {
		metrics.record(false)
		fmt.Println("FAIL  [LIST domain] — domain not indexed, can't LIST")
	} else {
		listPrice := uint64(100)
		txList := blockchain.NewListTransaction("ledger-test.bdns", listPrice, redeemTID,
			nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
			fee, nonceA, nodeA.TransactionPool)
		nodeA.BroadcastTransaction(*txList)
		fmt.Printf("[LIST] ledger-test.bdns — price=%d redeemsTID=%d\n", listPrice, redeemTID)

		waitBlocks(slotInterval, slotsPerEpoch, 2)

		// Check IsForSale
		qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
		qn.TxMutex.Lock()
		forSale := qn.IndexManager.IsForSale("ledger-test.bdns")
		lp := qn.IndexManager.GetListPrice("ledger-test.bdns")
		qn.TxMutex.Unlock()

		if forSale && lp == listPrice {
			metrics.record(true)
			fmt.Printf("PASS  [LIST domain] — forSale=true listPrice=%d\n", lp)
		} else {
			metrics.record(false)
			fmt.Printf("FAIL  [LIST domain] — forSale=%v listPrice=%d\n", forSale, lp)
		}
	}

	// Zero-price LIST rejection
	nonceA = getNonce(nodeA)
	txZeroList := blockchain.NewListTransaction("ledger-test.bdns", 0, redeemTID,
		nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	if txZeroList == nil {
		metrics.record(true)
		fmt.Println("PASS  [zero-price LIST] — constructor returned nil")
	} else {
		metrics.record(false)
		fmt.Println("FAIL  [zero-price LIST] — constructor did NOT return nil")
	}

	// Self-buy rejection
	nonceA = getNonce(nodeA)
	qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
	qn.TxMutex.Lock()
	listedTx := qn.IndexManager.GetDomain("ledger-test.bdns")
	selfBuyTID := 0
	if listedTx != nil {
		selfBuyTID = listedTx.TID
	}
	qn.TxMutex.Unlock()

	if selfBuyTID != 0 {
		txSelfBuy := blockchain.NewBuyTransaction("ledger-test.bdns", 100, selfBuyTID,
			nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
			fee, nonceA, nodeA.TransactionPool)
		nodeA.BroadcastTransaction(*txSelfBuy)
		fmt.Println("[BUY] self-buy attempt — expect rejection")

		waitBlocks(slotInterval, slotsPerEpoch, 1)

		// Owner should still be nodeA
		qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
		qn.TxMutex.Lock()
		owner := qn.IndexManager.GetOwner("ledger-test.bdns")
		qn.TxMutex.Unlock()
		ownerHex := hex.EncodeToString(owner)
		if ownerHex == pubA {
			metrics.record(true)
			fmt.Println("PASS  [self-buy] — owner unchanged")
		} else {
			metrics.record(false)
			fmt.Printf("FAIL  [self-buy] — owner changed to %s\n", ownerHex[:16])
		}
	} else {
		metrics.record(false)
		fmt.Println("SKIP  [self-buy] — domain not indexed")
	}

	// BUY from another node (nodeB buys from nodeA)
	nonceB := getNonce(nodeB)
	qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
	qn.TxMutex.Lock()
	buyTarget := qn.IndexManager.GetDomain("ledger-test.bdns")
	buyTID := 0
	if buyTarget != nil {
		buyTID = buyTarget.TID
	}
	qn.TxMutex.Unlock()

	if buyTID != 0 {
		txBuy := blockchain.NewBuyTransaction("ledger-test.bdns", 100, buyTID,
			nodeB.KeyPair.PublicKey, &nodeB.KeyPair.PrivateKey,
			fee, nonceB, nodeB.TransactionPool)
		nodeB.BroadcastTransaction(*txBuy)
		fmt.Printf("[BUY] nodeB buys ledger-test.bdns — maxPrice=100 fee=%d nonce=%d\n", fee, nonceB)

		waitBlocks(slotInterval, slotsPerEpoch, 2)

		qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
		qn.TxMutex.Lock()
		newOwner := qn.IndexManager.GetOwner("ledger-test.bdns")
		stillForSale := qn.IndexManager.IsForSale("ledger-test.bdns")
		qn.TxMutex.Unlock()

		newOwnerHex := hex.EncodeToString(newOwner)
		if newOwnerHex == pubB {
			metrics.record(true)
			fmt.Println("PASS  [BUY ownership] — owner is now nodeB")
		} else {
			metrics.record(false)
			fmt.Printf("FAIL  [BUY ownership] — owner=%s\n", newOwnerHex[:16])
		}
		if !stillForSale {
			metrics.record(true)
			fmt.Println("PASS  [BUY cleared listing] — no longer for sale")
		} else {
			metrics.record(false)
			fmt.Println("FAIL  [BUY cleared listing] — still listed!")
		}

		// Verify balance transfer
		balAPost := getBal(nodeA, pubA)
		fmt.Printf("[INFO] NodeA balance after BUY: %d\n", balAPost)
	} else {
		metrics.record(false)
		fmt.Println("FAIL  [BUY domain] — domain not indexed")
	}

	// Non-owner LIST rejection (nodeA tries to LIST what nodeB now owns)
	nonceA = getNonce(nodeA)
	qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
	qn.TxMutex.Lock()
	curTx := qn.IndexManager.GetDomain("ledger-test.bdns")
	nonOwnerTID := 0
	if curTx != nil {
		nonOwnerTID = curTx.TID
	}
	qn.TxMutex.Unlock()

	if nonOwnerTID != 0 {
		txBadList := blockchain.NewListTransaction("ledger-test.bdns", 200, nonOwnerTID,
			nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
			fee, nonceA, nodeA.TransactionPool)
		nodeA.BroadcastTransaction(*txBadList)
		fmt.Println("[LIST] nodeA (non-owner) tries to LIST — expect rejection")

		waitBlocks(slotInterval, slotsPerEpoch, 1)

		// Domain should NOT be listed
		qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
		qn.TxMutex.Lock()
		isListed := qn.IndexManager.IsForSale("ledger-test.bdns")
		qn.TxMutex.Unlock()
		if !isListed {
			metrics.record(true)
			fmt.Println("PASS  [non-owner LIST] — not listed")
		} else {
			metrics.record(false)
			fmt.Println("FAIL  [non-owner LIST] — listed by non-owner!")
		}
	} else {
		metrics.record(false)
		fmt.Println("FAIL  [non-owner LIST] — domain not indexed")
	}

	// TRANSFER domain (nodeB → nodeC)
	fmt.Println("\n=== Ledger Sim: Ownership Transfer Tests ===")

	// Read nodeB's nonce from leader (most up-to-date after BUY block)
	nonceB = nodeA.BalanceLedger.GetNonce(pubB)
	fmt.Printf("[INFO] nodeB nonce from leader: %d\n", nonceB)

	// Read TID from bestQueryNode
	qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
	qn.TxMutex.Lock()
	transferTx := qn.IndexManager.GetDomain("ledger-test.bdns")
	transferTID := 0
	if transferTx != nil {
		transferTID = transferTx.TID
	}
	transferOwnerHex := ""
	if transferTx != nil {
		transferOwnerHex = hex.EncodeToString(qn.IndexManager.GetOwner("ledger-test.bdns"))
	}
	qn.TxMutex.Unlock()
	fmt.Printf("[INFO] query node sees owner=%s (expect nodeB=%s)\n", transferOwnerHex[:16], pubB[:16])

	pubC := hex.EncodeToString(nodeC.KeyPair.PublicKey)

	if transferTID != 0 {
		txTransfer := blockchain.NewTransferTransaction("ledger-test.bdns", nodeC.KeyPair.PublicKey, transferTID,
			nodeB.KeyPair.PublicKey, &nodeB.KeyPair.PrivateKey,
			fee, nonceB, nodeB.TransactionPool)
		nodeB.BroadcastTransaction(*txTransfer)
		fmt.Printf("[TRANSFER] ledger-test.bdns nodeB→nodeC — fee=%d nonce=%d\n", fee, nonceB)

		waitBlocks(slotInterval, slotsPerEpoch, 2)

		qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
		qn.TxMutex.Lock()
		tOwner := qn.IndexManager.GetOwner("ledger-test.bdns")
		qn.TxMutex.Unlock()

		tOwnerHex := hex.EncodeToString(tOwner)
		if tOwnerHex == pubC {
			metrics.record(true)
			fmt.Println("PASS  [TRANSFER ownership] — owner is now nodeC")
		} else {
			metrics.record(false)
			fmt.Printf("FAIL  [TRANSFER ownership] — owner=%s\n", tOwnerHex[:16])
		}
	} else {
		metrics.record(false)
		fmt.Println("FAIL  [TRANSFER domain] — domain not indexed")
	}

	// DELIST — nodeC lists then delists
	nonceC := nodeA.BalanceLedger.GetNonce(pubC)
	qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
	qn.TxMutex.Lock()
	delistBase := qn.IndexManager.GetDomain("ledger-test.bdns")
	delistTID := 0
	if delistBase != nil {
		delistTID = delistBase.TID
	}
	qn.TxMutex.Unlock()

	if delistTID != 0 {
		txList2 := blockchain.NewListTransaction("ledger-test.bdns", 500, delistTID,
			nodeC.KeyPair.PublicKey, &nodeC.KeyPair.PrivateKey,
			fee, nonceC, nodeC.TransactionPool)
		nodeC.BroadcastTransaction(*txList2)
		fmt.Println("[LIST] nodeC lists ledger-test.bdns at 500")

		waitBlocks(slotInterval, slotsPerEpoch, 2)

		// Refresh TID after LIST
		qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
		qn.TxMutex.Lock()
		afterList := qn.IndexManager.GetDomain("ledger-test.bdns")
		delistTID2 := 0
		if afterList != nil {
			delistTID2 = afterList.TID
		}
		qn.TxMutex.Unlock()

		nonceC = nodeA.BalanceLedger.GetNonce(pubC)
		txDelist := blockchain.NewDelistTransaction("ledger-test.bdns", delistTID2,
			nodeC.KeyPair.PublicKey, &nodeC.KeyPair.PrivateKey,
			fee, nonceC, nodeC.TransactionPool)
		nodeC.BroadcastTransaction(*txDelist)
		fmt.Println("[DELIST] nodeC delists ledger-test.bdns")

		waitBlocks(slotInterval, slotsPerEpoch, 2)

		qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
		qn.TxMutex.Lock()
		stillListed := qn.IndexManager.IsForSale("ledger-test.bdns")
		qn.TxMutex.Unlock()

		if !stillListed {
			metrics.record(true)
			fmt.Println("PASS  [DELIST] — not for sale")
		} else {
			metrics.record(false)
			fmt.Println("FAIL  [DELIST] — still for sale!")
		}
	} else {
		metrics.record(false)
		fmt.Println("FAIL  [DELIST] — domain not indexed")
	}

	fmt.Println("\n=== Ledger Sim: RENEW Tests ===")
	nonceA = getNonce(nodeA)
	renewSalt := []byte("renew-ledger-salt")
	renewCommit := blockchain.NewCommitTransaction("renew-ledger.bdns",
		renewSalt, nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	nodeA.BroadcastTransaction(*renewCommit)
	fmt.Println("[COMMIT] renew-ledger.bdns")

	waitBlocks(slotInterval, slotsPerEpoch, int(blockchain.CommitMinDelay)+1)

	nonceA = getNonce(nodeA)
	nextBlock = nodeA.Blockchain.GetLatestBlock().Index + 1
	txFresh := blockchain.NewRevealTransaction("renew-ledger.bdns", renewSalt,
		[]blockchain.Record{{Type: "A", Value: "10.0.0.50", Priority: 0}},
		3600, nextBlock, slotsPerDay, renewCommit.TID,
		nodeA.KeyPair.PublicKey, nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey,
		fee, nonceA, nodeA.TransactionPool)
	nodeA.BroadcastTransaction(*txFresh)
	fmt.Println("[REVEAL] renew-ledger.bdns")

	waitBlocks(slotInterval, slotsPerEpoch, 2)

	qn = bestQueryNode(nodes, []string{"renew-ledger.bdns"})
	qn.TxMutex.Lock()
	renewBase := qn.IndexManager.GetDomain("renew-ledger.bdns")
	qn.TxMutex.Unlock()

	if renewBase != nil {
		oldExpiry := renewBase.ExpirySlot
		nonceA = getNonce(nodeA)
		txRenew := blockchain.NewRenewTransaction("renew-ledger.bdns",
			renewBase.Records, renewBase.CacheTTL,
			renewBase.ExpirySlot, slotsPerDay, renewBase.TID,
			nodeA.KeyPair.PublicKey, &nodeA.KeyPair.PrivateKey, nodeA.TransactionPool,
			fee, nonceA)
		nodeA.BroadcastTransaction(*txRenew)
		fmt.Printf("[RENEW] renew-ledger.bdns — oldExpiry=%d\n", oldExpiry)

		waitBlocks(slotInterval, slotsPerEpoch, 2)

		qn = bestQueryNode(nodes, []string{"renew-ledger.bdns"})
		qn.TxMutex.Lock()
		renewed := qn.IndexManager.GetDomain("renew-ledger.bdns")
		qn.TxMutex.Unlock()
		if renewed != nil && renewed.ExpirySlot > oldExpiry {
			metrics.record(true)
			fmt.Printf("PASS  [RENEW expiry] — %d→%d\n", oldExpiry, renewed.ExpirySlot)
		} else {
			metrics.record(false)
			fmt.Println("FAIL  [RENEW expiry] — expiry unchanged or domain missing")
		}
	} else {
		metrics.record(false)
		fmt.Println("FAIL  [RENEW] — base domain not indexed")
	}

	// Consensus — all nodes at the same height should have identical ledger hashes
	fmt.Println("\n=== Ledger Sim: Consensus Tests ===")
	waitBlocks(slotInterval, slotsPerEpoch, 2)
	refHash := hex.EncodeToString(nodes[0].BalanceLedger.Hash())
	nodes[0].BcMutex.Lock()
	refHeight := nodes[0].Blockchain.GetLatestBlock().Index
	nodes[0].BcMutex.Unlock()
	matchCount := 1
	mismatchCount := 0
	for i := 1; i < len(nodes); i++ {
		nodes[i].BcMutex.Lock()
		h := hex.EncodeToString(nodes[i].BalanceLedger.Hash())
		height := nodes[i].Blockchain.GetLatestBlock().Index
		nodes[i].BcMutex.Unlock()
		if height != refHeight {
			fmt.Printf("  SKIP node%d — height %d vs ref %d\n", i, height, refHeight)
			continue
		}
		if h != refHash {
			mismatchCount++
			fmt.Printf("  MISMATCH node0=%s.. vs node%d=%s..\n", refHash[:16], i, h[:16])
		} else {
			matchCount++
		}
	}
	if mismatchCount == 0 {
		metrics.record(true)
		fmt.Printf("PASS  [ledger consensus] — %d nodes at height %d agree\n", matchCount, refHeight)
	} else {
		metrics.record(false)
		fmt.Printf("FAIL  [ledger consensus] — %d match, %d mismatch\n", matchCount, mismatchCount)
	}

	// Double-spend rejection — two UPDATEs with the same RedeemsTxID
	nonceC = nodeA.BalanceLedger.GetNonce(pubC)
	qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
	qn.TxMutex.Lock()
	dsTx := qn.IndexManager.GetDomain("ledger-test.bdns")
	dsTID := 0
	if dsTx != nil {
		dsTID = dsTx.TID
	}
	qn.TxMutex.Unlock()

	if dsTID != 0 {
		txDS1 := blockchain.NewTransaction(blockchain.UPDATE, "ledger-test.bdns",
			[]blockchain.Record{{Type: "A", Value: "10.0.0.99", Priority: 0}},
			3600, 0, slotsPerDay, dsTID,
			nodeC.KeyPair.PublicKey, &nodeC.KeyPair.PrivateKey,
			nodeC.TransactionPool, fee, nonceC)
		nodeC.BroadcastTransaction(*txDS1)

		txDS2 := blockchain.NewTransaction(blockchain.UPDATE, "ledger-test.bdns",
			[]blockchain.Record{{Type: "A", Value: "10.0.0.100", Priority: 0}},
			3600, 0, slotsPerDay, dsTID,
			nodeC.KeyPair.PublicKey, &nodeC.KeyPair.PrivateKey,
			nodeC.TransactionPool, fee, nonceC+1)
		nodeC.BroadcastTransaction(*txDS2)
		fmt.Println("[UPDATE×2] two UPDATEs with same RedeemsTxID — second should be rejected")

		waitBlocks(slotInterval, slotsPerEpoch, 2)

		qn = bestQueryNode(nodes, []string{"ledger-test.bdns"})
		qn.TxMutex.Lock()
		dsResolved, _ := network.ResolveDomain("ledger-test.bdns", "A", qn, currentSlot, slotsPerDay)
		qn.TxMutex.Unlock()

		resolvedIP := ""
		if len(dsResolved) > 0 {
			resolvedIP = dsResolved[0].Value
		}
		if resolvedIP != "10.0.0.100" {
			metrics.record(true)
			fmt.Printf("PASS  [double-spend] — IP=%s (not 10.0.0.100)\n", resolvedIP)
		} else {
			metrics.record(false)
			fmt.Println("FAIL  [double-spend] — second UPDATE with reused TID accepted!")
		}
	} else {
		metrics.record(false)
		fmt.Println("FAIL  [double-spend] — domain not indexed")
	}

	// Print summary
	fmt.Println("\n=== Ledger Sim Results ===")
	fmt.Printf("  Ledger tests passed : %d\n", metrics.pass)
	fmt.Printf("  Ledger tests failed : %d\n", metrics.fail)

	network.NodesCleanup(nodes)
	fmt.Println("Ledger simulation completed.")
}

// --- helpers ---

func waitBlocks(slotInterval, slotsPerEpoch, epochs int) {
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch*epochs) * time.Second)
}

// ledgerMetrics tracks pass/fail counts for the ledger test.
type ledgerMetrics struct {
	pass int
	fail int
}

func (m *ledgerMetrics) record(ok bool) {
	if ok {
		m.pass++
	} else {
		m.fail++
	}
}

// waitForNonce polls until the node's committed nonce >= minNonce or timeout.
// func waitForNonce(node *network.Node, pubKeyHex string, minNonce uint64, timeout time.Duration) {
// 	deadline := time.Now().Add(timeout)
// 	for time.Now().Before(deadline) {
// 		if node.BalanceLedger.GetNonce(pubKeyHex) >= minNonce {
// 			return
// 		}
// 		time.Sleep(500 * time.Millisecond)
// 	}
// }
