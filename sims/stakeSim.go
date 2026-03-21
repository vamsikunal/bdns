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
		slotInterval  = 5
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

	// Wait for all STAKEs to be mined (3 full epochs)
	fmt.Println("[StakeSim] Waiting for STAKEs to be mined...")
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch*3) * time.Second)

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

	// Phase 4: UNSTAKE delay test
	fmt.Println("\n=== StakeSim: Phase 4 — UNSTAKE delay ===")

	// Use node[5] (lowest stake: 5000) as the UNSTAKE test subject
	unstakeNode := nodes[5]
	pubHex5 := hex.EncodeToString(unstakeNode.KeyPair.PublicKey)

	// Check current stake before unstake
	unstakeNode.StakeMutex.Lock()
	stakeBefore := unstakeNode.StakeMap.GetStake(pubHex5)
	unstakeNode.StakeMutex.Unlock()
	fmt.Printf("[UNSTAKE] node6 stake before: %d\n", stakeBefore)

	// Issue UNSTAKE transaction
	nonce5 := unstakeNode.BalanceLedger.GetNonce(pubHex5)
	unstakeTx := blockchain.NewUnstakeTransaction(5000,
		unstakeNode.KeyPair.PublicKey, &unstakeNode.KeyPair.PrivateKey,
		1, nonce5, unstakeNode.TransactionPool)
	unstakeNode.BroadcastTransaction(*unstakeTx)
	fmt.Printf("[UNSTAKE] node6 submitted UNSTAKE for 5000 coins\n")

	// Wait 2 epochs for the UNSTAKE to be mined
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch*2) * time.Second)

	// Verify: stake should be reduced
	unstakeNode.StakeMutex.Lock()
	stakeAfter := unstakeNode.StakeMap.GetStake(pubHex5)
	unstakeNode.StakeMutex.Unlock()
	fmt.Printf("[UNSTAKE] node6 stake after: %d\n", stakeAfter)

	if stakeAfter < stakeBefore {
		pass("[UNSTAKE] — stake reduced after UNSTAKE transaction")
	} else {
		fail("[UNSTAKE] — stake did not reduce after UNSTAKE transaction")
	}

	// Verify: balance should NOT have been credited yet (UnstakeDelaySlots not elapsed)
	liquidBefore := unstakeNode.BalanceLedger.GetBalance(pubHex5)
	fmt.Printf("[UNSTAKE] node6 liquid balance immediately after: %d (delay not elapsed)\n", liquidBefore)
	// We can't easily verify exact timing because UnstakeDelaySlots=1000 is very long,
	// but we verify the unstake queue has the pending entry.
	unstakeNode.StakeMutex.Lock()
	pendingAmt := unstakeNode.UnstakeQueue.GetPendingStake(pubHex5)
	unstakeNode.StakeMutex.Unlock()
	if pendingAmt > 0 {
		pass("[UNSTAKE delay] — coins are in UnstakeQueue (not yet liquid)")
	} else {
		fail("[UNSTAKE delay] — UnstakeQueue has no pending entry")
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
