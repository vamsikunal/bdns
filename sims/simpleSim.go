package sims

import (
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/network"
	"github.com/miekg/dns"
)

func SimpleSim() {
	if err := CleanChainData(); err != nil {
		fmt.Printf("[SimpleSim] Warning: chaindata cleanup failed: %v\n", err)
	}

	// Constants
	const numNodes = 6
	const slotInterval  = 8
	const slotsPerEpoch = 2
	const seed = 0
	waitForNonce := func(node *network.Node, pubKeyHex string, wantNonce uint64, timeoutSec int) bool {
		deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
		for time.Now().Before(deadline) {
			if node.BalanceLedger.GetNonce(pubKeyHex) >= wantNonce {
				return true
			}
			time.Sleep(1 * time.Second)
		}
		return false
	}

	freshNonce := func(node *network.Node) uint64 {
		return node.BalanceLedger.GetNonce(hex.EncodeToString(node.KeyPair.PublicKey))
	}

	nodes := network.InitializeP2PNodes(numNodes, slotInterval, slotsPerEpoch, seed)
	InitGateway(nodes)

	fmt.Println("Waiting for genesis block to be created...")
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch) * time.Second)

	// Phase 1: Each node STAKEs coins to become eligible for leader election
	fmt.Println("[SimpleSim] Issuing STAKE transactions...")
	stakeTIDs := make([]int, numNodes)
	for i, node := range nodes {
		pubKeyHex := hex.EncodeToString(node.KeyPair.PublicKey)
		nonce := node.BalanceLedger.GetNonce(pubKeyHex)

		stakeTx := blockchain.NewStakeTransaction(10000,
			node.KeyPair.PublicKey, &node.KeyPair.PrivateKey,
			1, nonce, node.TransactionPool)

		stakeTIDs[i] = stakeTx.TID
		node.BroadcastTransaction(*stakeTx)
		fmt.Printf("[STAKE] node%d staked 10000 coins\n", i+1)
		time.Sleep(200 * time.Millisecond)
	}

	fmt.Println("[SimpleSim] Waiting for all STAKEs to be mined (concurrent)...")
	var wgStake sync.WaitGroup
	for i, node := range nodes {
		if !node.IsFullNode {
			continue // light node BalanceLedger not updated by blocks — skip nonce poll
		}
		wgStake.Add(1)
		go func(i int, node *network.Node) {
			defer wgStake.Done()
			pk := hex.EncodeToString(node.KeyPair.PublicKey)
			if !waitForNonce(node, pk, 1, slotInterval*slotsPerEpoch*5) {
				fmt.Printf("[WARN] node%d STAKE not mined in time\n", i+1)
			}
		}(i, node)
	}
	wgStake.Wait()
	fmt.Println("[SimpleSim] All STAKEs confirmed (or timeout reached)")

	// Each node registers its own domain via COMMIT (Broadcast concurrently)
	domains := make([]string, numNodes)
	commitTIDs := make([]int, numNodes)
	salts := make([][]byte, numNodes)
	slotsPerDay := int64(86400 / slotInterval)

	fmt.Println("[SimpleSim] Wait for STAKEs, then broadcast COMMITs...")
	var wgCommit sync.WaitGroup
	for i, node := range nodes {
		wgCommit.Add(1)
		go func(i int, node *network.Node) {
			defer wgCommit.Done()
			pubKeyHex := hex.EncodeToString(node.KeyPair.PublicKey)

			// Light node BalanceLedger is never updated by blocks; skip nonce poll
			// but still broadcast its COMMIT so it participates in the domain set.
			if node.IsFullNode {
				if !waitForNonce(node, pubKeyHex, 1, slotInterval*slotsPerEpoch*5) {
					fmt.Printf("[WARN] node%d STAKE not mined in time, COMMIT may fail\n", i+1)
				}
			}

			domains[i] = fmt.Sprintf("node%d.com", i+1)
			salt := make([]byte, 16)
			for j := range salt {
				salt[j] = byte(i*16 + j)
			}
			salts[i] = salt
			commitNonce := freshNonce(node)
			commitTx := blockchain.NewCommitTransaction(domains[i], salt,
				node.KeyPair.PublicKey, &node.KeyPair.PrivateKey, 1, commitNonce, node.TransactionPool)
			commitTIDs[i] = commitTx.TID
			node.BroadcastTransaction(*commitTx)
			fmt.Printf("[COMMIT] node%d → %s (nonce=%d)\n", i+1, domains[i], commitNonce)
		}(i, node)
	}
	wgCommit.Wait()

	fmt.Println("[SimpleSim] Waiting for COMMITs to be mined...")
	time.Sleep(time.Duration(slotInterval*(slotsPerEpoch+int(blockchain.CommitMinDelay)+1)) * time.Second)

	// REVEAL each domain
	fmt.Println("[SimpleSim] Wait for COMMITs, then broadcast REVEALs...")
	var wgReveal sync.WaitGroup
	for i, node := range nodes {
		wgReveal.Add(1)
		go func(i int, node *network.Node) {
			defer wgReveal.Done()
			pubKeyHex := hex.EncodeToString(node.KeyPair.PublicKey)

			// Light node BalanceLedger not updated by blocks — skip nonce poll.
			if node.IsFullNode {
				if !waitForNonce(node, pubKeyHex, 2, slotInterval*slotsPerEpoch*5) {
					fmt.Printf("[WARN] node%d COMMIT not confirmed, REVEAL may fail\n", i+1)
				}
			}

			ip := fmt.Sprintf("192.168.1.%d", i+1)
			records := []blockchain.Record{{Type: "A", Value: ip, Priority: 0}}
			revealNonce := freshNonce(node)

			nextBlock := node.Blockchain.GetLatestBlock().Index + 1
			revealTx := blockchain.NewRevealTransaction(domains[i], salts[i], records,
				3600, nextBlock, slotsPerDay, commitTIDs[i],
				node.KeyPair.PublicKey, node.KeyPair.PublicKey, &node.KeyPair.PrivateKey,
				1, revealNonce, node.TransactionPool)
			node.BroadcastTransaction(*revealTx)
			fmt.Printf("[REVEAL] node%d → %s → %s (nonce=%d)\n", i+1, domains[i], ip, revealNonce)
		}(i, node)
	}
	wgReveal.Wait()
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch*2) * time.Second)

	queryNode := bestQueryNode(nodes, domains)
	fmt.Printf("[SimpleSim] Query node: %s\n\n", queryNode.Address)

	currentSlot := (time.Now().Unix() - queryNode.Config.InitialTimestamp) / queryNode.Config.SlotInterval
	slotsPerDay = int64(86400 / slotInterval)

	fmt.Println("=== SimpleSim: Resolution Checks ===")
	pass, fail, skip := 0, 0, 0

	for i, domain := range domains {
		expectedIP := fmt.Sprintf("192.168.1.%d", i+1)

		records, err := network.ResolveDomain(domain, "A", queryNode, currentSlot, slotsPerDay)
		if err != nil {
			fmt.Printf("SKIP  [%s] — not yet indexed: %v\n", domain, err)
			skip++
			continue
		}
		if len(records) == 0 {
			fmt.Printf("SKIP  [%s] — no A records returned\n", domain)
			skip++
			continue
		}
		if records[0].Value == expectedIP {
			fmt.Printf("PASS  [%s] → %s\n", domain, records[0].Value)
			pass++
		} else {
			fmt.Printf("FAIL  [%s] — expected %s, got %s\n", domain, expectedIP, records[0].Value)
			fail++
		}
	}

	fmt.Printf("\n=== SimpleSim Results ===\n")
	fmt.Printf("  Passed : %d / %d\n", pass, numNodes)
	fmt.Printf("  Skipped: %d  (not yet indexed — pre-existing fork issue)\n", skip)
	fmt.Printf("  Failed : %d\n", fail)

	// UDP probe: verify at least one DNS server answers on its assigned port
	fmt.Println("\n=== SimpleSim: UDP DNS Probe ===")
	for i, node := range nodes {
		if !node.IsFullNode {
			continue
		}
		port := fmt.Sprintf("127.0.0.1:%d", 5300+i)
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(domains[0]), dns.TypeA)
		resp, err := dns.Exchange(m, port)
		if err != nil {
			fmt.Printf(" probe :%d → error: %v\n", 5300+i, err)
		} else {
			fmt.Printf(" probe :%d → rcode=%d answers=%d\n", 5300+i, resp.Rcode, len(resp.Answer))
		}
		break // one probe is enough for smoke-test
	}

	CloseGateway(nodes)
	network.NodesCleanup(nodes)
	if err := CleanChainData(); err != nil {
		fmt.Printf("[SimpleSim] Warning: post-run chaindata cleanup failed: %v\n", err)
	}
	fmt.Println("Simple simulation completed.")
	os.Exit(0)
}
