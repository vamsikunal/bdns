package blockchain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// CommitOverlay is a copy-on-write wrapper over a CommitStore snapshot.
// Used during block validation — reads fall through to base, writes go to local buffers.
type CommitOverlay struct {
	base       map[string]*CommitRecord // snapshot (read-only)
	added      map[string]*CommitRecord // commits added in this block
	consumed   map[string]bool          // commits consumed by REVEAL in this block
	purged     map[string]bool          // commits purged (expired) in this block
	blockIndex int64                    // block being validated; used to reject expired base records
}

// NewCommitOverlay creates an overlay from a CommitStore snapshot.
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
	// Read-through to base; reject records expired at this block height
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
