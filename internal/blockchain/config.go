package blockchain

import (
	 "bytes"
)

// TrustedRegistries holds the public keys of trusted registries
var TrustedRegistries [][]byte

// GracePeriodDays is the number of days (slot) a domain stays in the grace period 
const GracePeriodDays int64 = 30

// CommitMinDelay is the minimum number of blocks between COMMIT and REVEAL.
const CommitMinDelay int64 = 3

// CommitMaxWindow is the maximum number of blocks a COMMIT remains valid.
const CommitMaxWindow int64 = 100

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
