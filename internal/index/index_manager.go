package index

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/bleasey/bdns/internal/blockchain"
)

// TxLocation tracks where a domain's transaction lives in the blockchain
type TxLocation struct {
	BlockIndex int64
	TxIndex    int
}

// revive:disable-next-line
type IndexManager struct {
	tree         *AVLTree
	filter       *BloomFilterManager
	expiryIndex  map[int64][]*blockchain.Transaction
	purgeIndex   map[int64][]*blockchain.Transaction
	txLocations  map[string]*TxLocation
	currentIndex int64
}

func NewIndexManager() *IndexManager {
	return &IndexManager{
		tree:         &AVLTree{nil},
		filter:       InitFilter(),
		expiryIndex:  make(map[int64][]*blockchain.Transaction),
		purgeIndex:   make(map[int64][]*blockchain.Transaction),
		txLocations:  make(map[string]*TxLocation),
		currentIndex: 0, // Corresponds to genesis block
	}
}

// Check if the domain is valid
func (im *IndexManager) GetDomain(domain string) *blockchain.Transaction {
	if !im.filter.IsValid(domain) {
		return nil
	}

	targetNode := im.tree.Search(HashDomain(domain))
	if targetNode == nil {
		return nil
	}

	return targetNode.value
}

// GetIndexHash computes a DETERMINISTIC index hash regardless of tree structure
func (im *IndexManager) GetIndexHash() []byte {
	records := im.tree.GetAllRecords()
	if len(records) == 0 {
		return nil
	}

	// Sort alphabetically by domain and Serialize
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

	// Hash the data
	hash := sha256.Sum256(data)
	return hash[:]
}

func (im *IndexManager) Add(domain string, tx *blockchain.Transaction, blockIndex int64, txIndex int, slotsPerDay int64) {
	im.tree.Add(HashDomain(domain), tx)
	im.filter.AddToValidList(domain)

	// Track transaction location for Merkle proof lookups
	im.txLocations[HashDomain(domain)] = &TxLocation{
		BlockIndex: blockIndex,
		TxIndex:    txIndex,
	}

	// Add to expiry index if ExpirySlot is set
	if tx.ExpirySlot > 0 {
		im.expiryIndex[tx.ExpirySlot] = append(im.expiryIndex[tx.ExpirySlot], tx)

		// Also index by PurgeSlot for auto-revocation
		purgeSlot := blockchain.ComputePurgeSlot(tx.ExpirySlot, slotsPerDay)
		im.purgeIndex[purgeSlot] = append(im.purgeIndex[purgeSlot], tx)
	}
}

func (im *IndexManager) Update(domain string, tx *blockchain.Transaction) {
	im.tree.Update(HashDomain(domain), tx)
}

func (im *IndexManager) Remove(domain string) {
	im.tree.Remove(HashDomain(domain))
	im.filter.AddToRevocationList(domain)
	delete(im.txLocations, HashDomain(domain))
}

// GetTxLocation returns the block/tx position for a domain (for Merkle proof generation)
func (im *IndexManager) GetTxLocation(domain string) *TxLocation {
	return im.txLocations[HashDomain(domain)]
}

// GetExpiredDomains returns all domains that expire at the given slot
func (im *IndexManager) GetExpiredDomains(currentSlot int64) []*blockchain.Transaction {
	return im.expiryIndex[currentSlot]
}

// GetPurgeableDomains returns domains that should be auto-revoked at the given slot
func (im *IndexManager) GetPurgeableDomains(currentSlot int64) []*blockchain.Transaction {
	return im.purgeIndex[currentSlot]
}

// RemoveFromExpiryIndex removes a domain from the expiry and purge indices (when manually revoked or renewed)
func (im *IndexManager) RemoveFromExpiryIndex(tx *blockchain.Transaction, slotsPerDay int64) {
	if tx.ExpirySlot > 0 {

		expiring := im.expiryIndex[tx.ExpirySlot]
		for i, t := range expiring {
			if t.TID == tx.TID {
				im.expiryIndex[tx.ExpirySlot] = append(expiring[:i], expiring[i+1:]...)
				break
			}
		}

		purgeSlot := blockchain.ComputePurgeSlot(tx.ExpirySlot, slotsPerDay)
		purging := im.purgeIndex[purgeSlot]
		for i, t := range purging {
			if t.TID == tx.TID {
				im.purgeIndex[purgeSlot] = append(purging[:i], purging[i+1:]...)
				break
			}
		}
	}
}

func HashDomain(domain string) string {
	hash := sha256.Sum256([]byte(domain))
	return hex.EncodeToString(hash[:])
}

// AddToExpiryAndPurgeIndex re-adds a renewed domain to expiry and purge indices
func (im *IndexManager) AddToExpiryAndPurgeIndex(tx *blockchain.Transaction, slotsPerDay int64) {
	if tx.ExpirySlot > 0 {
		im.expiryIndex[tx.ExpirySlot] = append(im.expiryIndex[tx.ExpirySlot], tx)
		purgeSlot := blockchain.ComputePurgeSlot(tx.ExpirySlot, slotsPerDay)
		im.purgeIndex[purgeSlot] = append(im.purgeIndex[purgeSlot], tx)
	}
}

func (im *IndexManager) IsForSale(domain string) bool {
	node := im.tree.Search(HashDomain(domain))
	return node != nil && node.metadata.ListPrice > 0
}

func (im *IndexManager) GetListPrice(domain string) uint64 {
	node := im.tree.Search(HashDomain(domain))
	if node == nil {
		return 0
	}
	return node.metadata.ListPrice
}

func (im *IndexManager) SetListPrice(domain string, price uint64) {
	node := im.tree.Search(HashDomain(domain))
	if node != nil {
		node.metadata.ListPrice = price
	}
}

func (im *IndexManager) SetOwner(domain string, ownerKey []byte) {
	node := im.tree.Search(HashDomain(domain))
	if node != nil {
		node.metadata.OwnerKey = ownerKey
	}
}

func (im *IndexManager) GetOwner(domain string) []byte {
	node := im.tree.Search(HashDomain(domain))
	if node == nil {
		return nil
	}
	return node.metadata.OwnerKey
}

func (im *IndexManager) GetTxByID(txID int) *blockchain.Transaction {
	node := im.tree.FindNodeByTxID(txID)
	if node == nil {
		return nil
	}
	return node.value
}
