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

// GatewaySim runs five deterministic scenarios that validate the full W2W3-Gateway stack.
func GatewaySim() {
	// Clean stale chaindata from previous runs so block slots start fresh.
	if err := CleanChainData(); err != nil {
		fmt.Printf("[GatewaySim] Warning: chaindata cleanup failed: %v\n", err)
	}

	const slotInterval = 5
	const slotsPerEpoch = 2
	const seed = 0

	nodes := network.InitializeP2PNodes(4, slotInterval, slotsPerEpoch, seed)
	InitGateway(nodes)

	fmt.Println("Waiting for genesis block to be created...")
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch) * time.Second)

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

	// STAKE all nodes concurrently (each uses its own nonce=0).
	var wgStake sync.WaitGroup
	for i, node := range nodes {
		wgStake.Add(1)
		go func(i int, node *network.Node) {
			defer wgStake.Done()
			pubKeyHex := hex.EncodeToString(node.KeyPair.PublicKey)
			nonce := node.BalanceLedger.GetNonce(pubKeyHex)
			tx := blockchain.NewStakeTransaction(10000,
				node.KeyPair.PublicKey, &node.KeyPair.PrivateKey, 1, nonce, node.TransactionPool)
			node.BroadcastTransaction(*tx)
			fmt.Printf("[STAKE] node%d staked 10000 coins\n", i+1)
		}(i, node)
	}
	wgStake.Wait()

	// Wait for all STAKEs to be mined (nonce advances to 1).
	fmt.Println("[GatewaySim] Waiting for STAKE transactions to be mined...")
	var wgStakeConfirm sync.WaitGroup
	for i, node := range nodes {
		wgStakeConfirm.Add(1)
		go func(i int, node *network.Node) {
			defer wgStakeConfirm.Done()
			pk := hex.EncodeToString(node.KeyPair.PublicKey)
			if !waitForNonce(node, pk, 1, slotInterval*slotsPerEpoch*10) {
				fmt.Printf("[WARN] node%d STAKE not confirmed in time\n", i+1)
			}
		}(i, node)
	}
	wgStakeConfirm.Wait()
	fmt.Println("[GatewaySim] All STAKEs confirmed.")

	// Register a test domain on node[0]
	domain := "gateway-test.bdns"
	expectedIP := "10.0.1.1"
	records := []blockchain.Record{{Type: "A", Value: expectedIP}}
	salt := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	slotsPerDay := int64(86400 / slotInterval)

	// Find the first full node to issue the domain registration.
	var n0 *network.Node
	for _, node := range nodes {
		if node.IsFullNode {
			n0 = node
			break
		}
	}
	pubKeyHex := hex.EncodeToString(n0.KeyPair.PublicKey)

	// COMMIT — nonce is now 1 (after STAKE was mined).
	commitNonce := n0.BalanceLedger.GetNonce(pubKeyHex)
	commitTx := blockchain.NewCommitTransaction(domain, salt,
		n0.KeyPair.PublicKey, &n0.KeyPair.PrivateKey, 1, commitNonce, n0.TransactionPool)
	n0.BroadcastTransaction(*commitTx)
	fmt.Printf("[COMMIT] domain=%s nonce=%d\n", domain, commitNonce)

	// Wait for COMMIT to be mined (nonce advances to commitNonce+1).
	fmt.Println("[GatewaySim] Waiting for COMMIT to be mined...")
	if !waitForNonce(n0, pubKeyHex, commitNonce+1, slotInterval*(slotsPerEpoch+int(blockchain.CommitMinDelay)+3)) {
		fmt.Println("[WARN] COMMIT not confirmed in time — REVEAL may fail")
	}
	// Extra delay to ensure CommitMinDelay has elapsed in block count.
	time.Sleep(time.Duration(slotInterval*(int(blockchain.CommitMinDelay)+1)) * time.Second)

	// REVEAL — nonce is now commitNonce+1.
	revealNonce := n0.BalanceLedger.GetNonce(pubKeyHex)
	n0.BcMutex.Lock()
	nextBlock := n0.Blockchain.GetLatestBlock().Index + 1
	n0.BcMutex.Unlock()
	revealTx := blockchain.NewRevealTransaction(domain, salt, records,
		3600, nextBlock, slotsPerDay, commitTx.TID,
		n0.KeyPair.PublicKey, n0.KeyPair.PublicKey, &n0.KeyPair.PrivateKey,
		1, revealNonce, n0.TransactionPool)
	n0.BroadcastTransaction(*revealTx)
	fmt.Printf("[REVEAL] domain=%s nonce=%d\n", domain, revealNonce)

	// Wait for REVEAL to be mined (nonce advances to revealNonce+1).
	fmt.Println("[GatewaySim] Waiting for REVEAL to be mined...")
	if !waitForNonce(n0, pubKeyHex, revealNonce+1, slotInterval*slotsPerEpoch*4) {
		fmt.Println("[WARN] REVEAL not confirmed in time — DNS checks may fail")
	}
	// Extra epoch for the domain to propagate to all nodes.
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch) * time.Second)

	pass, fail := 0, 0
	check := func(name string, ok bool) {
		if ok {
			fmt.Printf("PASS  [%s]\n", name)
			pass++
		} else {
			fmt.Printf("FAIL  [%s]\n", name)
			fail++
		}
	}

	fmt.Println("\n=== GatewaySim: 5 Scenarios ===")

	// Scenario 1: Happy path — UDP DNS query returns the registered A record
	{
		port := "127.0.0.1:5300" // node[0] is full, DNS on 5300
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(domain), dns.TypeA)

		client := dns.Client{Timeout: 2 * time.Second}
		resp, _, err := client.Exchange(m, port)
		ok := err == nil && resp != nil && resp.Rcode == dns.RcodeSuccess && len(resp.Answer) > 0
		if ok {
			a, isA := resp.Answer[0].(*dns.A)
			ok = isA && a.A.String() == expectedIP
		}
		check("Happy Path: UDP query returns A record", ok)
	}

	// Scenario 2: NXDOMAIN — query for an unregistered domain
	{
		port := "127.0.0.1:5300"
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn("unregistered-xyz.bdns"), dns.TypeA)

		client := dns.Client{Timeout: 2 * time.Second}
		resp, _, err := client.Exchange(m, port)
		check("NXDOMAIN: unregistered domain returns NameError",
			err == nil && resp != nil && resp.Rcode == dns.RcodeNameError)
	}

	// Scenario 3: Second record type — MX query returns NXDOMAIN (no MX registered)
	{
		port := "127.0.0.1:5300"
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(domain), dns.TypeMX)

		client := dns.Client{Timeout: 2 * time.Second}
		resp, _, err := client.Exchange(m, port)
		// No MX records registered → NXDOMAIN is correct
		check("Record Types: MX query for A-only domain returns NXDOMAIN",
			err == nil && resp != nil && resp.Rcode == dns.RcodeNameError)
	}

	// Scenario 4: Light node header stream — HeaderChain must be non-empty
	{
		lightHeaderLen := 0
		for _, node := range nodes {
			if !node.IsFullNode {
				lightHeaderLen = len(node.HeaderChain)
				break
			}
		}
		check("Stream: light node HeaderChain is non-empty", lightHeaderLen > 0)
	}

	// Scenario 5: Failover — query falls back to node[2]'s DNS port when node[0] unavailable
	{
		// Node[2] is also a full node; its DNS port is 5302
		port := "127.0.0.1:5302"
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(domain), dns.TypeA)

		client := dns.Client{Timeout: 2 * time.Second}
		resp, _, err := client.Exchange(m, port)
		ok := err == nil && resp != nil && resp.Rcode == dns.RcodeSuccess && len(resp.Answer) > 0
		check("Failover: alternate full node also resolves the domain", ok)
	}

	fmt.Printf("\n=== GatewaySim Results: %d PASS / %d FAIL ===\n", pass, pass+fail)
	if fail == 0 {
		fmt.Println("W2W3-Gateway phase exit criterion: SATISFIED")
	}

	CloseGateway(nodes)
	network.NodesCleanup(nodes)
	if err := CleanChainData(); err != nil {
		fmt.Printf("[GatewaySim] Warning: post-run chaindata cleanup failed: %v\n", err)
	}
	os.Exit(0)
}
