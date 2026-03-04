package index

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/bleasey/bdns/internal/blockchain"
)

type overlayEntry struct {
	tx           *blockchain.Transaction
	ownerKey     []byte
	listPrice    uint64
	added        bool
	ownerSet     bool
	listPriceSet bool
	blockIndex   int64
	txIndex      int
}

type IndexOverlay struct {
	real         *IndexManager
	journal      map[string]*overlayEntry
	pendingSpent map[int]bool
	slotsPerDay  int64
}

func NewIndexOverlay(real *IndexManager) *IndexOverlay {
	return &IndexOverlay{
		real:         real,
		journal:      make(map[string]*overlayEntry),
		pendingSpent: make(map[int]bool),
	}
}

func (o *IndexOverlay) GetDomain(domain string) *blockchain.Transaction {
	if entry, ok := o.journal[domain]; ok {
		return entry.tx
	}
	return o.real.GetDomain(domain)
}

func (o *IndexOverlay) Add(domain string, tx *blockchain.Transaction, blockIndex int64, txIndex int, slotsPerDay int64) {
	o.slotsPerDay = slotsPerDay
	entry, ok := o.journal[domain]
	if !ok {
		entry = &overlayEntry{}
		o.journal[domain] = entry
	}
	entry.tx = tx
	entry.added = true
	entry.blockIndex = blockIndex
	entry.txIndex = txIndex
}

func (o *IndexOverlay) SetOwner(domain string, ownerKey []byte) {
	entry, ok := o.journal[domain]
	if !ok {
		existing := o.real.GetDomain(domain)
		entry = &overlayEntry{tx: existing}
		o.journal[domain] = entry
	}
	entry.ownerKey = make([]byte, len(ownerKey))
	copy(entry.ownerKey, ownerKey)
	entry.ownerSet = true
}

func (o *IndexOverlay) GetOwner(domain string) []byte {
	if entry, ok := o.journal[domain]; ok && entry.ownerSet {
		return entry.ownerKey
	}
	return o.real.GetOwner(domain)
}

func (o *IndexOverlay) SetListPrice(domain string, price uint64) {
	entry, ok := o.journal[domain]
	if !ok {
		existing := o.real.GetDomain(domain)
		entry = &overlayEntry{tx: existing}
		o.journal[domain] = entry
	}
	entry.listPrice = price
	entry.listPriceSet = true
}

func (o *IndexOverlay) GetListPrice(domain string) uint64 {
	if entry, ok := o.journal[domain]; ok && entry.listPriceSet {
		return entry.listPrice
	}
	return o.real.GetListPrice(domain)
}

func (o *IndexOverlay) IsForSale(domain string) bool {
	if entry, ok := o.journal[domain]; ok && entry.listPriceSet {
		return entry.listPrice > 0
	}
	return o.real.IsForSale(domain)
}

func (o *IndexOverlay) GetTxByID(txID int) *blockchain.Transaction {
	for _, entry := range o.journal {
		if entry.tx != nil && entry.tx.TID == txID {
			return entry.tx
		}
	}
	return o.real.GetTxByID(txID)
}

func (o *IndexOverlay) MarkAsSpent(txID int) {
	o.pendingSpent[txID] = true
}

func (o *IndexOverlay) IsSpent(txID int) bool {
	if o.pendingSpent[txID] {
		return true
	}
	return o.real.IsSpent(txID)
}

func (o *IndexOverlay) GetIndexHash() []byte {
	realRecords := o.real.tree.GetAllRecords()

	merged := make(map[string]DomainRecord)
	for _, r := range realRecords {
		merged[r.Domain] = r
	}

	for domain, entry := range o.journal {
		hashed := HashDomain(domain)
		if entry.added && entry.tx != nil {
			rec := DomainRecord{
				Domain:     hashed,
				Records:    entry.tx.Records,
				ExpirySlot: entry.tx.ExpirySlot,
			}
			if entry.ownerSet {
				rec.OwnerKey = entry.ownerKey
			}
			if entry.listPriceSet {
				rec.ListPrice = entry.listPrice
			}
			merged[hashed] = rec
		} else if existing, ok := merged[hashed]; ok {
			if entry.ownerSet {
				existing.OwnerKey = entry.ownerKey
			}
			if entry.listPriceSet {
				existing.ListPrice = entry.listPrice
			}
			merged[hashed] = existing
		}
	}

	if len(merged) == 0 {
		return nil
	}

	records := make([]DomainRecord, 0, len(merged))
	for _, r := range merged {
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Domain < records[j].Domain
	})

	var data []byte
	for _, r := range records {
		var parts []string
		for _, rec := range r.Records {
			parts = append(parts, fmt.Sprintf("%s:%s:%d", rec.Type, rec.Value, rec.Priority))
		}
		recStr := "[" + strings.Join(parts, "|") + "]"
		ownerHex := hex.EncodeToString(r.OwnerKey)
		line := fmt.Sprintf("%s:%d:%s:%d:%s\n", r.Domain, r.ExpirySlot, recStr, r.ListPrice, ownerHex)
		data = append(data, []byte(line)...)
	}

	hash := sha256.Sum256(data)
	return hash[:]
}

func (o *IndexOverlay) Commit() {
	for domain, entry := range o.journal {
		if entry.added && entry.tx != nil {
			o.real.Add(domain, entry.tx, entry.blockIndex, entry.txIndex, o.slotsPerDay)
		}
		if entry.ownerSet {
			o.real.SetOwner(domain, entry.ownerKey)
		}
		if entry.listPriceSet {
			o.real.SetListPrice(domain, entry.listPrice)
		}
	}
	for txID := range o.pendingSpent {
		o.real.MarkAsSpent(txID)
	}
}
