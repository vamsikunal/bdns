package blockchain

import (
	"bytes"
)

// TrustedRegistries holds the public keys of trusted registries
var TrustedRegistries [][]byte

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
