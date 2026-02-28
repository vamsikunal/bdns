package blockchain

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type AccountState struct {
	Balance uint64
	Nonce   uint64
}

// BalanceLedger is the in-memory account-based balance book.
// Guarded by sync.RWMutex for concurrent access safety.
type BalanceLedger struct {
	mu       sync.RWMutex
	accounts map[string]*AccountState // key: hex-encoded public key
}

func NewBalanceLedger() *BalanceLedger {
	return &BalanceLedger{accounts: make(map[string]*AccountState)}
}

func (bl *BalanceLedger) Seed(pubKeyHex string, initialBalance uint64) {
	bl.accounts[pubKeyHex] = &AccountState{Balance: initialBalance, Nonce: 0}
}

func (bl *BalanceLedger) GetBalance(pubKeyHex string) uint64 {
	bl.mu.RLock()
	defer bl.mu.RUnlock()
	if acc, ok := bl.accounts[pubKeyHex]; ok {
		return acc.Balance
	}
	return 0
}

func (bl *BalanceLedger) GetNonce(pubKeyHex string) uint64 {
	bl.mu.RLock()
	defer bl.mu.RUnlock()
	if acc, ok := bl.accounts[pubKeyHex]; ok {
		return acc.Nonce
	}
	return 0
}

func (bl *BalanceLedger) Debit(pubKeyHex string, amount uint64) error {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	if bl.getBalanceUnsafe(pubKeyHex) < amount {
		return fmt.Errorf("insufficient balance: %s has %d, needs %d",
			pubKeyHex[:8], bl.getBalanceUnsafe(pubKeyHex), amount)
	}
	bl.ensureAccountUnsafe(pubKeyHex)
	bl.accounts[pubKeyHex].Balance -= amount
	return nil
}

func (bl *BalanceLedger) Credit(pubKeyHex string, amount uint64) {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	bl.ensureAccountUnsafe(pubKeyHex)
	bl.accounts[pubKeyHex].Balance += amount
}

func (bl *BalanceLedger) IncrementNonce(pubKeyHex string) {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	bl.ensureAccountUnsafe(pubKeyHex)
	bl.accounts[pubKeyHex].Nonce++
}

func (bl *BalanceLedger) ensureAccountUnsafe(pubKeyHex string) {
	if _, ok := bl.accounts[pubKeyHex]; !ok {
		bl.accounts[pubKeyHex] = &AccountState{}
	}
}

// Lock-free helper — caller must hold bl.mu.
func (bl *BalanceLedger) getBalanceUnsafe(pubKeyHex string) uint64 {
	if acc, ok := bl.accounts[pubKeyHex]; ok {
		return acc.Balance
	}
	return 0
}

// Hash returns a deterministic SHA-256 of all accounts, sorted by public key.
func (bl *BalanceLedger) Hash() []byte {
	bl.mu.RLock()
	defer bl.mu.RUnlock()

	type entry struct {
		key   string
		state *AccountState
	}
	entries := make([]entry, 0, len(bl.accounts))
	for k, v := range bl.accounts {
		entries = append(entries, entry{k, v})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].key < entries[j].key
	})

	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("%s:%d:%d\n", e.key, e.state.Balance, e.state.Nonce))
	}
	h := sha256.Sum256([]byte(sb.String()))
	return h[:]
}

func (bl *BalanceLedger) Clone() *BalanceLedger {
	bl.mu.RLock()
	defer bl.mu.RUnlock()
	clone := NewBalanceLedger()
	for k, v := range bl.accounts {
		copied := *v // copy the struct, not the pointer
		clone.accounts[k] = &copied
	}
	return clone
}
