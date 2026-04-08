package blockchain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
)

// CommitRecord tracks a single on-chain COMMIT awaiting its REVEAL.
type CommitRecord struct {
	CommitHash  []byte // Length-prefixed SHA256 hash
	CommitterPK []byte // Public key of the committer
	CommitBlock int64  // Block index when COMMIT was included
	CommitSlot  int64  // Time-based slot number
	CommitTID   int    // TID of the COMMIT transaction
	ExpiryBlock int64  // Block index for expiration
}

type CommitStore struct {
	mu      sync.RWMutex
	pending map[string]*CommitRecord // key: hex(CommitHash)
}

func NewCommitStore() *CommitStore {
	return &CommitStore{
		pending: make(map[string]*CommitRecord),
	}
}

// AddCommit records a new COMMIT in the pending store.
func (cs *CommitStore) AddCommit(record *CommitRecord) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	key := hex.EncodeToString(record.CommitHash)
	cs.pending[key] = record
}

// GetCommit retrieves a pending commit by its hash (hex-encoded).
func (cs *CommitStore) GetCommit(commitHashHex string) *CommitRecord {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.pending[commitHashHex]
}

// ConsumeCommit removes a pending commit after a successful REVEAL.
func (cs *CommitStore) ConsumeCommit(commitHashHex string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.pending, commitHashHex)
}

// PurgeExpired removes all commits whose ExpiryBlock has been surpassed.
func (cs *CommitStore) PurgeExpired(currentBlockIndex int64) int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	purged := 0
	for key, record := range cs.pending {
		if currentBlockIndex > record.ExpiryBlock {
			delete(cs.pending, key)
			purged++
		}
	}
	return purged
}

// Hash produces a deterministic SHA-256 hash of the commit store.
func (cs *CommitStore) Hash() []byte {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	keys := make([]string, 0, len(cs.pending))
	for k := range cs.pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		rec := cs.pending[k]
		h.Write(rec.CommitHash)
		h.Write(rec.CommitterPK)
		h.Write(IntToByteArr(rec.CommitBlock))
		h.Write(IntToByteArr(rec.CommitSlot))
		h.Write(IntToByteArr(int64(rec.CommitTID)))
		h.Write(IntToByteArr(rec.ExpiryBlock))
	}

	return h.Sum(nil)
}

// Clone produces a deep copy of the CommitStore.
func (cs *CommitStore) Clone() *CommitStore {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	clone := &CommitStore{
		pending: make(map[string]*CommitRecord, len(cs.pending)),
	}
	for k, rec := range cs.pending {
		clonedRec := *rec
		clonedRec.CommitHash = append([]byte(nil), rec.CommitHash...)
		clonedRec.CommitterPK = append([]byte(nil), rec.CommitterPK...)
		clone.pending[k] = &clonedRec
	}
	return clone
}

// ExportPending returns a shallow copy of the pending map.
func (cs *CommitStore) ExportPending() map[string]*CommitRecord {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	snapshot := make(map[string]*CommitRecord, len(cs.pending))
	for k, v := range cs.pending {
		snapshot[k] = v
	}
	return snapshot
}

type CommitOverlay struct {
	base       map[string]*CommitRecord // snapshot (read-only)
	added      map[string]*CommitRecord // commits added in this block
	consumed   map[string]bool          // commits consumed by REVEAL in this block
	purged     map[string]bool          // commits purged (expired) in this block
	blockIndex int64                    // block being validated
}

func NewCommitOverlay(base map[string]*CommitRecord, blockIndex int64) *CommitOverlay {
	return &CommitOverlay{
		base:       base,
		added:      make(map[string]*CommitRecord),
		consumed:   make(map[string]bool),
		purged:     make(map[string]bool),
		blockIndex: blockIndex,
	}
}

func (co *CommitOverlay) AddCommit(record *CommitRecord) {
	key := hex.EncodeToString(record.CommitHash)
	co.added[key] = record
}

func (co *CommitOverlay) GetCommit(commitHashHex string) *CommitRecord {
	if co.consumed[commitHashHex] || co.purged[commitHashHex] {
		return nil
	}
	if rec, ok := co.added[commitHashHex]; ok {
		return rec
	}
	rec := co.base[commitHashHex]
	if rec != nil && co.blockIndex > rec.ExpiryBlock {
		return nil
	}
	return rec
}

func (co *CommitOverlay) ConsumeCommit(commitHashHex string) {
	co.consumed[commitHashHex] = true
	delete(co.added, commitHashHex)
}

func (co *CommitOverlay) PurgeExpired(currentBlockIndex int64) int {
	purged := 0
	for key, rec := range co.base {
		if co.consumed[key] {
			continue
		}
		if currentBlockIndex > rec.ExpiryBlock {
			co.purged[key] = true
			purged++
		}
	}
	for key, rec := range co.added {
		if currentBlockIndex > rec.ExpiryBlock {
			delete(co.added, key)
			purged++
		}
	}
	return purged
}

func (co *CommitOverlay) Hash() []byte {
	merged := make(map[string]*CommitRecord, len(co.base)+len(co.added))
	for k, v := range co.base {
		if !co.consumed[k] && !co.purged[k] {
			merged[k] = v
		}
	}
	for k, v := range co.added {
		merged[k] = v
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		rec := merged[k]
		h.Write(rec.CommitHash)
		h.Write(rec.CommitterPK)
		h.Write(IntToByteArr(rec.CommitBlock))
		h.Write(IntToByteArr(rec.CommitSlot))
		h.Write(IntToByteArr(int64(rec.CommitTID)))
		h.Write(IntToByteArr(rec.ExpiryBlock))
	}
	return h.Sum(nil)
}

// Commit flushes the overlay's mutations into a real CommitStore.
func (co *CommitOverlay) Commit(target *CommitStore) {
	target.mu.Lock()
	defer target.mu.Unlock()

	for k, v := range co.added {
		target.pending[k] = v
	}
	for k := range co.consumed {
		delete(target.pending, k)
	}
	for k := range co.purged {
		delete(target.pending, k)
	}
}
