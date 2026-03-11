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

// StakeMap tracks staked balances for all active validators.
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
