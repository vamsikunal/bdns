package blockchain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
)

// StakeRecord holds the staked balance for a single validator.
type StakeRecord struct {
	StakedBalance uint64
}

func (r StakeRecord) DeepCopy() StakeRecord {
	return StakeRecord{StakedBalance: r.StakedBalance}
}

type StakeMap struct {
	mu      sync.RWMutex
	entries map[string]StakeRecord // hex-encoded validator public key -> record
}

func NewStakeMap() *StakeMap {
	return &StakeMap{entries: make(map[string]StakeRecord)}
}

func (sm *StakeMap) AddStake(validatorHex string, amount uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	r := sm.entries[validatorHex]
	r.StakedBalance += amount
	sm.entries[validatorHex] = r
}

// ReduceStake decreases the balance; deletes the entry when it reaches zero.
func (sm *StakeMap) ReduceStake(validatorHex string, amount uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	r, ok := sm.entries[validatorHex]
	if !ok {
		return
	}
	if amount >= r.StakedBalance {
		delete(sm.entries, validatorHex)
		return
	}
	r.StakedBalance -= amount
	sm.entries[validatorHex] = r
}

// GetStake returns the staked balance for validatorHex, or 0 if not found.
func (sm *StakeMap) GetStake(validatorHex string) uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.entries[validatorHex].StakedBalance
}

// GetAll returns a snapshot map of all staked balances; used by leader election.
func (sm *StakeMap) GetAll() map[string]uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make(map[string]uint64, len(sm.entries))
	for k, v := range sm.entries {
		out[k] = v.StakedBalance
	}
	return out
}

// HasAnyStake returns true if at least one validator has a staked balance.
func (sm *StakeMap) HasAnyStake() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.entries) > 0
}

// Hash returns a deterministic SHA-256 digest of the stake map (keys sorted).
func (sm *StakeMap) Hash() []byte {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	keys := make([]string, 0, len(sm.entries))
	for k, v := range sm.entries {
		if v.StakedBalance > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		decoded, _ := hex.DecodeString(k)
		h.Write(decoded)
		h.Write(IntToByteArr(int64(sm.entries[k].StakedBalance)))
	}
	return h.Sum(nil)
}

// Clone returns a deep copy of the StakeMap as a StakeStorer.
func (sm *StakeMap) Clone() StakeStorer {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	clone := NewStakeMap()
	for k, v := range sm.entries {
		clone.entries[k] = v.DeepCopy()
	}
	return clone
}

// PendingUnstake represents a single queued unstake entry awaiting maturity.
type PendingUnstake struct {
	ValidatorHex string
	Amount       uint64
	MatureSlot   uint64 // slot at which coins become liquid
}

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
