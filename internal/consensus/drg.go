package consensus

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
)

const DRGSecretBytes = 32

// SecretValues holds the 256-bit commitment scheme values for one validator.
type SecretValues struct {
	U          []byte // 32-byte random output revealed in the reveal phase
	R          []byte // 32-byte blinding factor, kept secret until reveal
	Commitment []byte // SHA-256(senderPK || R || U), broadcast in commitment phase
}

// RevealData is the payload transmitted in a RevealMsg.
type RevealData struct {
	U []byte
	R []byte
}

// CommitmentPhase generates a SecretValues for the given sender.
func CommitmentPhase(senderPubKey []byte) (SecretValues, error) {
	u := make([]byte, DRGSecretBytes)
	if _, err := rand.Read(u); err != nil {
		return SecretValues{}, fmt.Errorf("CommitmentPhase: crypto/rand failed reading U: %w", err)
	}
	r := make([]byte, DRGSecretBytes)
	if _, err := rand.Read(r); err != nil {
		return SecretValues{}, fmt.Errorf("CommitmentPhase: crypto/rand failed reading R: %w", err)
	}

	// Commitment: SHA-256(senderPubKey || R || U)
	data := make([]byte, 0, len(senderPubKey)+DRGSecretBytes+DRGSecretBytes)
	data = append(data, senderPubKey...)
	data = append(data, r...)
	data = append(data, u...)
	h := sha256.Sum256(data)

	return SecretValues{U: u, R: r, Commitment: h[:]}, nil
}

// VerifyCommitment checks that the revealed U and R match the stored commitment.
func VerifyCommitment(senderPubKey []byte, sv SecretValues) bool {
	if len(sv.R) != DRGSecretBytes || len(sv.U) != DRGSecretBytes {
		return false
	}
	data := make([]byte, 0, len(senderPubKey)+DRGSecretBytes+DRGSecretBytes)
	data = append(data, senderPubKey...)
	data = append(data, sv.R...)
	data = append(data, sv.U...)
	computed := sha256.Sum256(data)
	return subtle.ConstantTimeCompare(computed[:], sv.Commitment) == 1
}

// VerifyAndCollectReveals batch-validates reveals against stored commitments.
func VerifyAndCollectReveals(
	reveals map[string]RevealData,
	commitments map[string][]byte,
	pubKeys map[string][]byte,
) (map[string][]byte, bool) {
	validated := make(map[string][]byte)
	for sender, rd := range reveals {
		commitment, hasCom := commitments[sender]
		pubKey, hasPK := pubKeys[sender]
		if !hasCom || !hasPK {
			continue
		}
		sv := SecretValues{U: rd.U, R: rd.R, Commitment: commitment}
		if VerifyCommitment(pubKey, sv) {
			validated[sender] = rd.U
		}
	}
	return validated, len(validated) > 0
}

// RecoveryPhase XORs all validated 32-byte U values and normalizes to [0,1).
func RecoveryPhase(validatedUs map[string][]byte) float64 {
	if len(validatedUs) == 0 {
		// Returns 0.0 with a warning if no valid U values are provided.
		return 0.0
	}
	xored := make([]byte, DRGSecretBytes)
	for _, u := range validatedUs {
		if len(u) == DRGSecretBytes {
			for i := range xored {
				xored[i] ^= u[i]
			}
		}
	}
	// Extract leading 8 bytes as uint64 and normalize to [0,1)
	v := uint64(0)
	for i := 0; i < 8; i++ {
		v = (v << 8) | uint64(xored[i])
	}
	return float64(v) / float64(^uint64(0))
}
