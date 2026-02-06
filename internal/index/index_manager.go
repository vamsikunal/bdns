package index

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

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
	txLocations  map[string]*TxLocation              
	currentIndex int64
}

func NewIndexManager() *IndexManager {
	return &IndexManager{
		tree:         &AVLTree{nil},
		filter:       InitFilter(),
		expiryIndex:  make(map[int64][]*blockchain.Transaction),
		txLocations:  make(map[string]*TxLocation),
		currentIndex: 0, // Corresponds to genesis block
	}
}

func (im *IndexManager) GetIP(domain string) *blockchain.Transaction {
	// Check if the domain is valid
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
		data = append(data, []byte(r.Domain+":"+r.IP+"\n")...)
	}

	// Hash the data
	hash := sha256.Sum256(data)
	return hash[:]
}

func (im *IndexManager) Add(domain string, tx *blockchain.Transaction, blockIndex int64, txIndex int) {
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

// RemoveFromExpiryIndex removes a domain from the expiry index (when manually revoked early)
func (im *IndexManager) RemoveFromExpiryIndex(tx *blockchain.Transaction) {
	if tx.ExpirySlot > 0 {
		expiring := im.expiryIndex[tx.ExpirySlot]
		for i, t := range expiring {
			if t.TID == tx.TID {
				im.expiryIndex[tx.ExpirySlot] = append(expiring[:i], expiring[i+1:]...)
				break
			}
		}
	}
}

func HashDomain(domain string) string {
	hash := sha256.Sum256([]byte(domain))
	return hex.EncodeToString(hash[:])
}
