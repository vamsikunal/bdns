package sims

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/network"
)

// StakeSim: to check PoS staking, leader election, and unstake mechanics

// Network: 6 nodes, 5s slots, 2 slots/epoch.

func StakeSim() {
	const (
		numNodes      = 6
		slotInterval  = 8
		slotsPerEpoch = 2
		seed          = 71
	)

	type stakeResult struct {
		pass int
		fail int
	}
	res := &stakeResult{}
	pass := func(msg string) {
		res.pass++
		fmt.Println("PASS ", msg)
	}
	fail := func(msg string) {
		res.fail++
		fmt.Println("FAIL ", msg)
	}

	nodes := network.InitializeP2PNodes(numNodes, slotInterval, slotsPerEpoch, seed)
	defer network.NodesCleanup(nodes)

	// waitForNonce polls until a node's nonce reaches wantNonce or timeout elapses.
	waitForNonce := func(node *network.Node, pubKeyHex string, wantNonce uint64, timeoutSec int) bool {
		deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
		for time.Now().Before(deadline) {
			if node.BalanceLedger.GetNonce(pubKeyHex) >= wantNonce {
				return true
			}
			time.Sleep(500 * time.Millisecond)
		}
		return false
	}

	fmt.Println("=== StakeSim: waiting for genesis block ===")
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch) * time.Second)

	// Phase 1: Stake validators with differentiated amounts
	fmt.Println("\n=== StakeSim: Phase 1 — weighted staking ===")

	stakeAmounts := []uint64{10000, 20000, 30000, 15000, 25000, 5000}
	for i, node := range nodes {
		pubKeyHex := hex.EncodeToString(node.KeyPair.PublicKey)
		nonce := node.BalanceLedger.GetNonce(pubKeyHex)

		stakeTx := blockchain.NewStakeTransaction(stakeAmounts[i],
			node.KeyPair.PublicKey, &node.KeyPair.PrivateKey,
			1, nonce, node.TransactionPool)

		node.BroadcastTransaction(*stakeTx)
		fmt.Printf("[STAKE] node%d → %d coins\n", i+1, stakeAmounts[i])
		time.Sleep(150 * time.Millisecond)
	}

	// Wait for all full-node STAKEs to be mined — light node's BalanceLedger
	// is never updated by block commits (it only stores headers), so its nonce
	// stays at 0 permanently and waitForNonce would hang until timeout.
	fmt.Println("[StakeSim] Waiting for STAKEs to be mined...")
	for i, node := range nodes {
		if !node.IsFullNode {
			continue // light node ledger not updated by blocks — skip nonce poll
		}
		pubKeyHex := hex.EncodeToString(node.KeyPair.PublicKey)
		expectedNonce := uint64(1) // all full nodes start at nonce 0; STAKE advances to 1
		if !waitForNonce(node, pubKeyHex, expectedNonce, slotInterval*slotsPerEpoch*5) {
			fmt.Printf("[WARN] node%d STAKE not confirmed in time\n", i+1)
		}
	}

	// Phase 2: StakeMapHash consensus check
	fmt.Println("\n=== StakeSim: Phase 2 — StakeMapHash consensus ===")

	// Collect hashes from all nodes
	hashes := make([]string, numNodes)
	for i, node := range nodes {
		node.StakeMutex.Lock()
		h := hex.EncodeToString(node.StakeMap.Hash())
		node.StakeMutex.Unlock()
		hashes[i] = h
		fmt.Printf("  node%d stake hash prefix: %s...\n", i+1, safePrefix(h, 12))
	}

	ref := hashes[0]
	allMatch := true
	for i := 1; i < numNodes; i++ {
		if hashes[i] != ref {
			allMatch = false
			fmt.Printf("  MISMATCH: node1 vs node%d\n", i+1)
		}
	}
	if allMatch {
		pass("[StakeMapHash consensus] — all nodes agree")
	} else {
		fail("[StakeMapHash consensus] — nodes disagree on stake state")
	}

	// Phase 3: Weighted leader election distribution
	fmt.Println("\n=== StakeSim: Phase 3 — weighted leader election (observe) ===")

	// Run for 6 epochs and count how many times each node leads
	leaderCount := make(map[string]int)
	totalEpochs := 6
	epochDuration := time.Duration(slotInterval*slotsPerEpoch) * time.Second

	for e := 0; e < totalEpochs; e++ {
		time.Sleep(epochDuration)
		for _, node := range nodes {
			if node.Blockchain == nil {
				continue
			}
			node.BcMutex.Lock()
			latest := node.Blockchain.GetLatestBlock()
			node.BcMutex.Unlock()
			if latest != nil {
				leaderHex := hex.EncodeToString(latest.SlotLeader)
				leaderCount[leaderHex]++
			}
		}
	}

	totalLeads := 0
	for _, n := range nodes {
		totalLeads += leaderCount[hex.EncodeToString(n.KeyPair.PublicKey)]
	}

	fmt.Printf("[Leader election] Total block observations: %d\n", totalLeads)
	if totalLeads > 0 {
		for i, node := range nodes {
			pubHex := hex.EncodeToString(node.KeyPair.PublicKey)
			count := leaderCount[pubHex]
			pct := 0.0
			if totalLeads > 0 {
				pct = float64(count) / float64(totalLeads) * 100
			}
			fmt.Printf("  node%d stake=%d obs=%d (%.1f%% of observations)\n",
				i+1, stakeAmounts[i], count, pct)
		}
		pass("[Leader election] — completed without panic")
	} else {
		fail("[Leader election] — no blocks produced during observation window")
	}

	// Phase 4: UNSTAKE delay test — use the node that actually has staked coins
	fmt.Println("\n=== StakeSim: Phase 4 — UNSTAKE delay ===")

	// Find a node whose STAKE was confirmed (stake > 0)
	var unstakeNode *network.Node
	var pubHex5 string
	var stakedAmt uint64
	for i, node := range nodes {
		ph := hex.EncodeToString(node.KeyPair.PublicKey)
		node.StakeMutex.Lock()
		amt := node.StakeMap.GetStake(ph)
		node.StakeMutex.Unlock()
		if amt > 0 {
			unstakeNode = node
			pubHex5 = ph
			stakedAmt = amt
			fmt.Printf("[UNSTAKE] using node%d (stake=%d)\n", i+1, amt)
			break
		}
	}

	if unstakeNode == nil {
		fail("[UNSTAKE] — no node has confirmed stake; STAKE txs may not have been mined")
		fail("[UNSTAKE delay] — skipped (no confirmed stake)")
	} else {
		// Check current stake before unstake
		unstakeNode.StakeMutex.Lock()
		stakeBefore := unstakeNode.StakeMap.GetStake(pubHex5)
		unstakeNode.StakeMutex.Unlock()
		fmt.Printf("[UNSTAKE] stake before: %d\n", stakeBefore)

		// Issue UNSTAKE for the confirmed amount
		nonce5 := unstakeNode.BalanceLedger.GetNonce(pubHex5)
		unstakeTx := blockchain.NewUnstakeTransaction(stakedAmt,
			unstakeNode.KeyPair.PublicKey, &unstakeNode.KeyPair.PrivateKey,
			1, nonce5, unstakeNode.TransactionPool)
		unstakeNode.BroadcastTransaction(*unstakeTx)
		fmt.Printf("[UNSTAKE] submitted UNSTAKE for %d coins\n", stakedAmt)

		// Wait for UNSTAKE to be mined (nonce advances).
		// 3-slot timeout (24s) is generous for localhost gossip delivery.
		if !waitForNonce(unstakeNode, pubHex5, nonce5+1, slotInterval*3) {
			fmt.Println("[WARN] UNSTAKE not mined in time")
		}

		// Verify: stake should be reduced
		unstakeNode.StakeMutex.Lock()
		stakeAfter := unstakeNode.StakeMap.GetStake(pubHex5)
		unstakeNode.StakeMutex.Unlock()
		fmt.Printf("[UNSTAKE] stake after: %d\n", stakeAfter)

		if stakeAfter < stakeBefore {
			pass("[UNSTAKE] — stake reduced after UNSTAKE transaction")
		} else {
			fail("[UNSTAKE] — stake did not reduce after UNSTAKE transaction")
		}

		// Verify: balance should NOT have been credited yet (UnstakeDelaySlots not elapsed)
		liquidBefore := unstakeNode.BalanceLedger.GetBalance(pubHex5)
		fmt.Printf("[UNSTAKE] liquid balance immediately after: %d (delay not elapsed)\n", liquidBefore)
		unstakeNode.StakeMutex.Lock()
		pendingAmt := unstakeNode.UnstakeQueue.GetPendingStake(pubHex5)
		unstakeNode.StakeMutex.Unlock()
		if pendingAmt > 0 {
			pass("[UNSTAKE delay] — coins are in UnstakeQueue (not yet liquid)")
		} else {
			fail("[UNSTAKE delay] — UnstakeQueue has no pending entry")
		}
	}

	// Phase 5: Final StakeMapHash check after UNSTAKE
	fmt.Println("\n=== StakeSim: Phase 5 — StakeMapHash after UNSTAKE ===")

	time.Sleep(time.Duration(slotInterval*slotsPerEpoch) * time.Second)

	hashes2 := make([]string, numNodes)
	for i, node := range nodes {
		node.StakeMutex.Lock()
		h := hex.EncodeToString(node.StakeMap.Hash())
		node.StakeMutex.Unlock()
		hashes2[i] = h
	}
	ref2 := hashes2[0]
	allMatch2 := true
	for i := 1; i < numNodes; i++ {
		if hashes2[i] != ref2 {
			allMatch2 = false
		}
	}
	if allMatch2 {
		pass("[StakeMapHash post-UNSTAKE] — all nodes agree on updated stake state")
	} else {
		fail("[StakeMapHash post-UNSTAKE] — nodes disagree after UNSTAKE")
	}

	// Summary
	fmt.Println("\n=== StakeSim Results ===")
	fmt.Printf("  Passed: %d\n", res.pass)
	fmt.Printf("  Failed: %d\n", res.fail)
	fmt.Println("Stake simulation completed.")
}

// safePrefix returns the first n chars of s, or s if shorter.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
