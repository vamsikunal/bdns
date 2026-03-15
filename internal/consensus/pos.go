package consensus

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"log"
)

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
		// Fallback: SHA-256("drg-fallback:" || prevBlockHash || BigEndian(slot))
		buf := make([]byte, 8)
		// slot included to prevent same leader every slot during genesis
		binary.BigEndian.PutUint64(buf, uint64(slot))
		data := append([]byte("drg-fallback:"), prevBlockHash...)
		data = append(data, buf...)
		h := sha256.Sum256(data)
		seed = float64(binary.BigEndian.Uint64(h[:8])) / float64(^uint64(0))
		log.Printf("GetSlotLeaderUtil: using fallback seed for slot %d", slot)
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
