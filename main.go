package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/bleasey/bdns/sims"
)

const (
	numNodes         = 10
	txTime           = 20 * time.Second
	simulationTime   = 60 * time.Second
	interval         = 1 * time.Second
	slotInterval     = 5
	slotsPerEpoch    = 2
	seed             = 0
	txProbability    = 0.05
	queryProbability = 0.02
	renewProbability = 0.02
)

func main() {
	simName := flag.String("sim", "rand", "simulation to run: simple | feature | ledger | rand | stake")
	flag.Parse()

	switch *simName {
	case "simple":
		sims.SimpleSim()
	case "feature":
		sims.FeatureSim()
	case "ledger":
		sims.LedgerSim()
	case "stake":
		sims.StakeSim()
	default:
		sims.RandSim(numNodes, txTime, simulationTime, interval, slotInterval, slotsPerEpoch, seed, txProbability, queryProbability, renewProbability)
		if err := sims.CleanChainData(); err != nil {
			fmt.Printf("Warning: Error cleaning chaindata: %v\n", err)
		}
	}
}
