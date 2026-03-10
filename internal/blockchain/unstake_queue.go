package blockchain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// PendingUnstake represents a single queued unstake entry awaiting maturity.
type PendingUnstake struct {
	ValidatorHex string
	Amount       uint64
	MatureSlot   uint64 // slot at which coins become liquid
}

// UnstakeQueue holds all pending unstake entries across all validators.
type UnstakeQueue struct {
	pending []PendingUnstake
}

func NewUnstakeQueue() *UnstakeQueue {
	return &UnstakeQueue{}
}

func (uq *UnstakeQueue) Enqueue(validatorHex string, amount uint64, matureSlot uint64) {
	uq.pending = append(uq.pending, PendingUnstake{validatorHex, amount, matureSlot})
}

// SweepMature removes and returns all entries with MatureSlot <= currentSlot.
func (uq *UnstakeQueue) SweepMature(currentSlot uint64) []PendingUnstake {
	remaining := make([]PendingUnstake, 0, len(uq.pending))
	matured := make([]PendingUnstake, 0)
	for _, e := range uq.pending {
		if e.MatureSlot <= currentSlot {
			matured = append(matured, e)
		} else {
			remaining = append(remaining, e)
		}
	}
	uq.pending = remaining
	return matured
}

// GetPendingStake returns the total amount queued for validatorHex across all entries.
func (uq *UnstakeQueue) GetPendingStake(validatorHex string) uint64 {
	var total uint64
	for _, e := range uq.pending {
		if e.ValidatorHex == validatorHex {
			total += e.Amount
		}
	}
	return total
}

// BurnPending destroys up to amount coins from validatorHex's entries (FIFO).
// Returns the actual amount burned, which may be less than requested if the
// queue holds fewer coins than amount.
func (uq *UnstakeQueue) BurnPending(validatorHex string, amount uint64) uint64 {
	var burned uint64
	for i := range uq.pending {
		if burned >= amount {
			break
		}
		e := &uq.pending[i]
		if e.ValidatorHex != validatorHex {
			continue
		}
		need := amount - burned
		if e.Amount <= need {
			burned += e.Amount
			e.Amount = 0
		} else {
			e.Amount -= need
			burned += need
		}
	}
	// Compact zero-amount entries
	out := uq.pending[:0]
	for _, e := range uq.pending {
		if e.Amount > 0 {
			out = append(out, e)
		}
	}
	uq.pending = out
	return burned
}

// Hash returns a deterministic SHA-256 digest of the queue (sorted by MatureSlot, ValidatorHex, Amount).
func (uq *UnstakeQueue) Hash() []byte {
	sorted := make([]PendingUnstake, len(uq.pending))
	copy(sorted, uq.pending)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].MatureSlot != sorted[j].MatureSlot {
			return sorted[i].MatureSlot < sorted[j].MatureSlot
		}
		if sorted[i].ValidatorHex != sorted[j].ValidatorHex {
			return sorted[i].ValidatorHex < sorted[j].ValidatorHex
		}
		return sorted[i].Amount < sorted[j].Amount
	})

	h := sha256.New()
	for _, e := range sorted {
		decoded, _ := hex.DecodeString(e.ValidatorHex)
		h.Write(decoded)
		h.Write(IntToByteArr(int64(e.Amount)))
		h.Write(IntToByteArr(int64(e.MatureSlot)))
	}
	return h.Sum(nil)
}

// Clone returns a deep copy of the UnstakeQueue.
func (uq *UnstakeQueue) Clone() *UnstakeQueue {
	clone := &UnstakeQueue{
		pending: make([]PendingUnstake, len(uq.pending)),
	}
	copy(clone.pending, uq.pending)
	return clone
}
