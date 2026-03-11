package blockchain

import (
	 "bytes"
)

// TrustedRegistries holds the public keys of trusted registries
var TrustedRegistries [][]byte

// GracePeriodDays is the number of days (slot) a domain stays in the grace period 
const GracePeriodDays int64 = 30

// Staking protocol constants
const (
	MinStakeThreshold    uint64 = 100             // minimum coins required to become a validator
	UnstakeDelaySlots    uint64 = 1000            // slots before unstaked coins become liquid
	SlashingPercent      uint64 = 100             // percentage of stake slashed for equivocation
	MaxEvidenceBlockBytes int    = 2 * 1024 * 1024 // max byte size of a single evidence block
)

// ComputeSlashAmount returns the amount to slash given a total stake and percent.
// Guards against overflow when stake * percent would exceed uint64.
func ComputeSlashAmount(stake, percent uint64) uint64 {
	if percent == 0 {
		return 0
	}
	if stake > ^uint64(0)/percent {
		return stake // saturate at full stake on overflow
	}
	return stake * percent / 100
}


// InitTrustedRegistries initializes the trusted registry keys
func InitTrustedRegistries(registryKeys [][]byte) {
	TrustedRegistries = make([][]byte, len(registryKeys))
	copy(TrustedRegistries, registryKeys)
}

func IsRegistryKey(pubKey []byte) bool {
	for _, regKey := range TrustedRegistries {
		if bytes.Equal(regKey, pubKey) {
			return true
		}
	}
	return false
}

// it calculates the slot at which a domain is auto-revoked.
func ComputePurgeSlot(expirySlot int64, slotsPerDay int64) int64 {
	return expirySlot + (GracePeriodDays * slotsPerDay)
}

// GetDomainPhase returns the current phase of a domain given the current slot.
func GetDomainPhase(currentSlot int64, expirySlot int64, slotsPerDay int64) string {
	if currentSlot < expirySlot {
		return "active"
	}
	purgeSlot := ComputePurgeSlot(expirySlot, slotsPerDay)
	if currentSlot < purgeSlot {
		return "grace"
	}
	return "purged" // Returns: "active", "grace", or "purged"
}
