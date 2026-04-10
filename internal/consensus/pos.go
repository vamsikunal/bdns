package consensus

import (
	"encoding/binary"
	"encoding/hex"
)

// DRGMockSeed produces a deterministic but well-distributed seed by XOR-ing all
// validator public keys with the slot number.
func DRGMockSeed(validators [][]byte, slot int64) []byte {
	seed := make([]byte, 32)
	for _, pubkey := range validators {
		for i := range seed {
			seed[i] ^= pubkey[i%len(pubkey)]
		}
	}
	slotBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(slotBytes, uint64(slot))
	for i := range slotBytes {
		seed[i] ^= slotBytes[i]
	}
	return seed
}

// GetStakes returns each validator's proportional weight from stakeMap.
func GetStakes(registryKeys [][]byte, stakeData map[string]uint64) map[string]float64 {
	stakeProbs := make(map[string]float64)

	// Compute full sum over all registries
	sum := uint64(0)
	for _, key := range registryKeys {
		sum += stakeData[hex.EncodeToString(key)]
	}

	if sum == 0 {
		// Genesis epoch — uniform weight over registry count, not stakeData count
		if len(registryKeys) == 0 {
			return stakeProbs
		}
		equalProb := 1.0 / float64(len(registryKeys))
		for _, key := range registryKeys {
			stakeProbs[hex.EncodeToString(key)] = equalProb
		}
		return stakeProbs
	}

	for _, key := range registryKeys {
		keyHex := hex.EncodeToString(key)
		stakeProbs[keyHex] = float64(stakeData[keyHex]) / float64(sum)
	}
	return stakeProbs
}

// GetSlotLeaderUtil selects the slot leader via a CDF walk over stake weights.
func GetSlotLeaderUtil(registryKeys [][]byte, stakeData map[string]uint64,
	epochRandoms map[string][]byte, prevBlockHash []byte, slot int64) (string, float64) {

	if len(registryKeys) == 0 {
		return "", 0.0
	}

	// Pre-compute CDF once — prevents floating-point drift from per-iteration recomputation
	probs := GetStakes(registryKeys, stakeData)
	cdf := make([]float64, len(registryKeys))
	cumulative := 0.0
	for i, key := range registryKeys {
		cumulative += probs[hex.EncodeToString(key)]
		cdf[i] = cumulative
	}

	// Derive DRG seed
	var seed float64
	if len(epochRandoms) > 0 {
		// XOR all validated U values
		xored := make([]byte, 32)
		for _, u := range epochRandoms {
			if len(u) == 32 {
				for i := range xored {
					xored[i] ^= u[i]
				}
			}
		}
		seed = float64(binary.BigEndian.Uint64(xored[:8])) / float64(^uint64(0))
	} else {
		// XOR-entropy mock: rotates leader election across all validators
		seedBytes := DRGMockSeed(registryKeys, slot)
		seed = float64(binary.BigEndian.Uint64(seedBytes[:8])) / float64(^uint64(0))
	}

	// CDF walk to select leader
	for i, key := range registryKeys {
		if seed <= cdf[i] {
			return hex.EncodeToString(key), seed
		}
	}

	// Rounding guard — last validator wins ties
	last := registryKeys[len(registryKeys)-1]
	return hex.EncodeToString(last), seed
}
