package sims

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bleasey/bdns/client"
	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/metrics"
	"github.com/bleasey/bdns/internal/network"
)

func CleanChainData() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %v", err)
	}

	projectRoot := filepath.Dir(cwd)

	err = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}

		if info.IsDir() && info.Name() == "chaindata" {
			fmt.Printf("Removing chaindata directory at: %s\n", path)
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("failed to remove chaindata at %s: %v", path, err)
			}
		}
		return nil
	})

	if err != nil && os.IsNotExist(err) {
		fmt.Println("Note: No chaindata directories found to clean")
		return nil
	}

	fmt.Println("Cleaned all chaindata directories")
	return nil
}

func RandSim(numNodes int, txTime time.Duration, simulationTime time.Duration, interval time.Duration,
	slotInterval int, slotsPerEpoch int, seed int, txProbability float64, queryProbability float64,
	renewProbability float64) {
	var wg sync.WaitGroup

	nodes := network.InitializeP2PNodes(numNodes, slotInterval, slotsPerEpoch, seed)

	fmt.Println("Waiting for genesis block to be created...")
	time.Sleep(time.Duration(slotInterval) * time.Second)

	LatencyTimes := make([]time.Duration, 0)
	var latencyMu sync.Mutex

	domains := make([]string, 0) // list of registered domains
	var domainsMu sync.Mutex     // guards domains

	txOnlyTime := time.Now().Add(txTime)
	simStopTime := time.Now().Add(simulationTime)
	var totalQueries int64
	var totalTxns int64

	metrics := metrics.GetDNSMetrics()

	wg.Add(numNodes)

	for i, node := range nodes {
		go func(node *network.Node, id int) {
			defer wg.Done()

			for time.Now().Before(simStopTime) {
				// Chance to create transaction
				if rand.Float64() < txProbability {
					domainsMu.Lock()
					domain := fmt.Sprintf("tx%d-node%d.com", len(domains), id+1)
					domainsMu.Unlock()

					ip := fmt.Sprintf("10.0.%d.%d", id+1, rand.Intn(255))
					ttl := int64(3600)
					records := []blockchain.Record{{Type: "A", Value: ip, Priority: 0}}
							tx := blockchain.NewTransaction(blockchain.REGISTER, domain, records, ttl, 0, 17280, 0, node.KeyPair.PublicKey, &node.KeyPair.PrivateKey, node.TransactionPool, 0, 0)
					node.BroadcastTransaction(*tx)
					fmt.Printf("Node %d sent transaction for domain %s\n", id+1, domain)

					domainsMu.Lock()
					domains = append(domains, domain)
					domainsMu.Unlock()

					atomic.AddInt64(&totalTxns, 1)
				}

				// Chance to renew a previously registered domain
				domainsMu.Lock()
				nDomains := len(domains)
				domainsMu.Unlock()

				if nDomains > 0 && rand.Float64() < renewProbability {
					domainsMu.Lock()
					target := rand.Intn(len(domains))
					domain := domains[target]
					domainsMu.Unlock()

					// Look up the current registration to get TID, ExpirySlot, OwnerKey
					oldTx := node.IndexManager.GetDomain(domain)
					if oldTx != nil {
						slotsPerDay := int64(86400 / slotInterval)

						// Copy the existing records to carry them forward on renewal
						recordsCopy := make([]blockchain.Record, len(oldTx.Records))
						copy(recordsCopy, oldTx.Records)

						tx := blockchain.NewRenewTransaction(
							domain,
							recordsCopy,
							oldTx.CacheTTL,
							oldTx.ExpirySlot,
							slotsPerDay,
							oldTx.TID,
							node.KeyPair.PublicKey, // registry key
							&node.KeyPair.PrivateKey,
							node.TransactionPool,
							0, 0,
						)
						node.BroadcastTransaction(*tx)
						fmt.Printf("Node %d renewed domain %s (old expiry: %d, new expiry: %d)\n",
							id+1, domain, oldTx.ExpirySlot, tx.ExpirySlot)
						atomic.AddInt64(&totalTxns, 1)
					}
				}

				// Chance to send a DNS request
				domainsMu.Lock()
				nDomainsQ := len(domains)
				domainsMu.Unlock()

				if time.Now().After(txOnlyTime) && nDomainsQ > 0 && rand.Float64() < queryProbability {
					domainsMu.Lock()
					target := rand.Intn(len(domains))
					domain := domains[target]
					domainsMu.Unlock()

					startTime := time.Now()
					node.MakeDNSRequest(domain, metrics)
					endTime := time.Now()

					latencyMu.Lock()
					LatencyTimes = append(LatencyTimes, endTime.Sub(startTime))
					latencyMu.Unlock()

					atomic.AddInt64(&totalQueries, 1)
				}

				time.Sleep(interval)
			}
		}(node, i)
	}

	wg.Wait()
	time.Sleep(10 * time.Second) // wait until nodes are ready
	client.RunAutoClient(domains)

	totalTime := 0.0

	for _, latency := range LatencyTimes {
		totalTime += float64(latency.Nanoseconds()) / 1000000
	}

	avgTime := totalTime / float64(len(LatencyTimes))
	TxnsPerSec := float64(totalTxns) / simulationTime.Seconds()
	QueriesPerSec := float64(totalQueries) / simulationTime.Seconds()

	fmt.Printf("Average time to resolve domain: %f ms\n", avgTime)
	fmt.Printf("Total queries: %d\n", totalQueries)
	fmt.Printf("Total txns: %d\n", totalTxns)
	fmt.Printf("Txns per second: %f\n", TxnsPerSec)
	fmt.Printf("Queries per second: %f\n", QueriesPerSec)

	metrics.PrintMetrics()
	network.NodesCleanup(nodes)

	if err := CleanChainData(); err != nil {
		fmt.Printf("Error cleaning chaindata: %v\n", err)
	}

	fmt.Println("Simulation completed")
	time.Sleep(5 * time.Second)
}
