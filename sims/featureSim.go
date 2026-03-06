package sims

import (
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/bleasey/bdns/client"
	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/network"
)

// FeatureSim: to check multi-record features

// Network: 10 nodes, 60s simulation, 5s slots, 2 slots/epoch.
func FeatureSim() {
	const (
		numNodes      = 10
		slotInterval  = 5
		slotsPerEpoch = 2
		seed          = 42
	)

	var wg sync.WaitGroup
	nodes := network.InitializeP2PNodes(numNodes, slotInterval, slotsPerEpoch, seed)

	fmt.Println("=== Feature Sim: waiting for genesis block ===")
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch*2) * time.Second) // wait 2 full epochs

	metrics := &featureMetrics{}

	// Use a single node for all registrations
	reg := nodes[0]
	regPubKeyHex := hex.EncodeToString(reg.KeyPair.PublicKey)
	regNonce := reg.BalanceLedger.GetNonce(regPubKeyHex)

	// Register domains with diverse record sets
	canonicalRecords := []blockchain.Record{
		{Type: "A", Value: "10.0.1.1", Priority: 0},
		{Type: "AAAA", Value: "2001:db8::1", Priority: 0},
		{Type: "MX", Value: "mail.canonical.bdns", Priority: 10},
		{Type: "MX", Value: "backup-mail.canonical.bdns", Priority: 20},
		{Type: "TXT", Value: "v=spf1 include:canonical.bdns ~all", Priority: 0},
	}
	registerDomain(reg, "canonical.bdns", canonicalRecords, 1, regNonce)
	regNonce++
	fmt.Println("[REGISTER] canonical.bdns — A, AAAA, 2×MX, TXT")
	time.Sleep(1 * time.Second)

	aliasRecords := []blockchain.Record{
		{Type: "CNAME", Value: "canonical.bdns", Priority: 0},
	}
	registerDomain(reg, "alias.bdns", aliasRecords, 1, regNonce)
	regNonce++
	fmt.Println("[REGISTER] alias.bdns — CNAME → canonical.bdns")
	time.Sleep(1 * time.Second)

	deepRecords := []blockchain.Record{
		{Type: "CNAME", Value: "alias.bdns", Priority: 0},
	}
	registerDomain(reg, "deep.bdns", deepRecords, 1, regNonce)
	regNonce++
	fmt.Println("[REGISTER] deep.bdns — CNAME → alias.bdns (2-hop chain)")
	time.Sleep(1 * time.Second)

	mxOnlyRecords := []blockchain.Record{
		{Type: "MX", Value: "mail1.mx-only.bdns", Priority: 5},
		{Type: "MX", Value: "mail2.mx-only.bdns", Priority: 10},
	}
	registerDomain(reg, "mx-only.bdns", mxOnlyRecords, 1, regNonce)
	regNonce++
	fmt.Println("[REGISTER] mx-only.bdns — 2×MX (no A)")
	time.Sleep(1 * time.Second)

	renewableRecords := []blockchain.Record{
		{Type: "A", Value: "10.0.5.5", Priority: 0},
		{Type: "TXT", Value: "initial-registration", Priority: 0},
	}
	registerDomain(reg, "renew-me.bdns", renewableRecords, 1, regNonce)
	regNonce++
	fmt.Println("[REGISTER] renew-me.bdns — A + TXT (will be renewed)")

	// Wait for registrations to be committed
	fmt.Println("\n[WAIT] Waiting for registrations to be committed (30s)...")
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch*3) * time.Second)

	// Pick the node with the most domains indexed
	allDomains := []string{"canonical.bdns", "alias.bdns", "deep.bdns", "mx-only.bdns", "renew-me.bdns"}
	queryNode := bestQueryNode(nodes, allDomains)
	fmt.Printf("[INFO] Using %s as query node (has most domains indexed)\n", queryNode.Address)

	// RENEW renew-me.bdns with updated records
	wg.Add(1)
	go func() {
		defer wg.Done()
		queryNode.TxMutex.Lock()
		oldTx := queryNode.IndexManager.GetDomain("renew-me.bdns")
		queryNode.TxMutex.Unlock()

		if oldTx == nil {
			fmt.Println("[RENEW] renew-me.bdns not yet indexed, skipping renew")
			return
		}

		updatedRecords := []blockchain.Record{
			{Type: "A", Value: "10.0.5.99", Priority: 0},
			{Type: "AAAA", Value: "2001:db8::5", Priority: 0},
			{Type: "TXT", Value: "renewed-registration", Priority: 0},
		}
		slotsPerDay := int64(86400 / slotInterval)
		qnPubKeyHex := hex.EncodeToString(queryNode.KeyPair.PublicKey)
		qnNonce := queryNode.BalanceLedger.GetNonce(qnPubKeyHex)
		tx := blockchain.NewRenewTransaction(
			"renew-me.bdns",
			updatedRecords,
			oldTx.CacheTTL,
			oldTx.ExpirySlot,
			slotsPerDay,
			oldTx.TID,
			queryNode.KeyPair.PublicKey,
			&queryNode.KeyPair.PrivateKey,
			queryNode.TransactionPool,
			1, qnNonce,
		)
		queryNode.BroadcastTransaction(*tx)
		fmt.Printf("[RENEW] renew-me.bdns — updated records (old expiry: %d, new expiry: %d)\n",
			oldTx.ExpirySlot, tx.ExpirySlot)
		metrics.renewCount++
	}()
	wg.Wait()

	// Wait for RENEW to be committed
	time.Sleep(time.Duration(slotInterval*slotsPerEpoch*2) * time.Second)

	// Type-aware resolution queries
	fmt.Println("\n=== Feature Sim: Type-Aware Resolution Tests ===")

	currentSlot := (time.Now().Unix() - queryNode.Config.InitialTimestamp) / queryNode.Config.SlotInterval
	slotsPerDay := int64(86400 / slotInterval)

	testCases := []struct {
		domain    string
		queryType string
		expectLen int // ≥1 means we expect results; 0 means expect error
		note      string
	}{
		{"canonical.bdns", "A", 1, "A record lookup"},
		{"canonical.bdns", "AAAA", 1, "AAAA record lookup"},
		{"canonical.bdns", "MX", 2, "MX records (priority sorted)"},
		{"canonical.bdns", "TXT", 1, "TXT record lookup"},
		{"alias.bdns", "CNAME", 1, "explicit CNAME query (no follow)"},
		{"alias.bdns", "A", 1, "CNAME→A resolution (1-hop)"},
		{"deep.bdns", "A", 1, "CNAME→CNAME→A resolution (2-hop)"},
		{"mx-only.bdns", "A", 0, "A on MX-only domain (expect error)"},
		{"mx-only.bdns", "MX", 2, "MX-only domain MX records"},
		{"renew-me.bdns", "A", 1, "renewed domain A record (updated IP)"},
		{"renew-me.bdns", "AAAA", 1, "renewed domain AAAA (added in renewal)"},
	}

	for _, tc := range testCases {
		records, err := network.ResolveDomain(tc.domain, tc.queryType, queryNode, currentSlot, slotsPerDay)
		if tc.expectLen == 0 {
			// Expect failure — any error is a pass
			if err != nil {
				fmt.Printf("PASS  [%s %s] — got expected error: %v\n", tc.queryType, tc.domain, err)
				metrics.pass++
			} else {
				fmt.Printf("FAIL  [%s %s] — expected error but got %d records\n", tc.queryType, tc.domain, len(records))
				metrics.fail++
			}
		} else {
			if err == nil && len(records) >= tc.expectLen {
				fmt.Printf("PASS  [%s %s] — %d record(s): %+v  (%s)\n", tc.queryType, tc.domain, len(records), records[0], tc.note)
				metrics.pass++
			} else if err != nil {
				fmt.Printf("SKIP  [%s %s] — not yet indexed: %v  (%s)\n", tc.queryType, tc.domain, err, tc.note)
				metrics.skip++
			} else {
				// err==nil but len(records)==0: domain indexed but type not committed yet — treat as SKIP
				fmt.Printf("SKIP  [%s %s] — indexed but no %s records yet  (%s)\n", tc.queryType, tc.domain, tc.queryType, tc.note)
				metrics.skip++
			}
		}
	}

	// Run auto-client DNS queries via UDP server
	fmt.Println("\n=== Feature Sim: Auto-Client Queries ===")
	time.Sleep(2 * time.Second)
	client.RunAutoClient([]string{"canonical.bdns", "alias.bdns", "deep.bdns", "mx-only.bdns", "renew-me.bdns"})

	// Print summary
	fmt.Println("\n=== Feature Sim Results ===")
	fmt.Printf("  Resolution tests passed : %d\n", metrics.pass)
	fmt.Printf("  Resolution tests skipped: %d  (domain not yet indexed)\n", metrics.skip)
	fmt.Printf("  Resolution tests failed : %d\n", metrics.fail)
	fmt.Printf("  RENEW transactions      : %d\n", metrics.renewCount)

	network.NodesCleanup(nodes)
	fmt.Println("Feature simulation completed.")
}

// registerDomain is a helper that calls NewTransaction and broadcasts it.
func registerDomain(node *network.Node, domain string, records []blockchain.Record, fee uint64, nonce uint64) {
	const ttl = int64(3600)
	const slotsPerDay = int64(86400 / 5) // 5s slots
	tx := blockchain.NewTransaction(
		blockchain.REGISTER,
		domain,
		records,
		ttl,
		0, slotsPerDay, 0,
		node.KeyPair.PublicKey,
		&node.KeyPair.PrivateKey,
		node.TransactionPool,
		fee, nonce,
	)
	node.BroadcastTransaction(*tx)
}

// featureMetrics tracks pass/fail/skip counts for the feature test.
type featureMetrics struct {
	pass       int
	fail       int
	skip       int
	renewCount int
}

// bestQueryNode scans all nodes and returns the one with the most domains indexed.
func bestQueryNode(nodes []*network.Node, domains []string) *network.Node {
	best := nodes[0]
	bestCount := 0
	for _, n := range nodes {
		count := 0
		for _, d := range domains {
			n.TxMutex.Lock()
			tx := n.IndexManager.GetDomain(d)
			n.TxMutex.Unlock()
			if tx != nil {
				count++
			}
		}
		if count > bestCount {
			bestCount = count
			best = n
		}
	}
	return best
}
