package blockchain

// ExpiryChecker provides expiration data for block validation
// Implemented by index.IndexManager to avoid circular dependency between packages
type ExpiryChecker interface {
	GetExpiredDomains(slotNumber int64) []*Transaction
	GetPurgeableDomains(slotNumber int64) []*Transaction
}

// DomainIndexer abstracts domain index operations for apply.go and ValidateTransactions.
type DomainIndexer interface {
	GetDomain(domain string) *Transaction
	Add(domain string, tx *Transaction, blockIndex int64, txIndex int, slotsPerDay int64)
	SetOwner(domain string, ownerKey []byte)
	GetOwner(domain string) []byte
	SetListPrice(domain string, price uint64)
	GetListPrice(domain string) uint64
	IsForSale(domain string) bool
	GetTxByID(txID int) *Transaction
	MarkAsSpent(txID int)
	IsSpent(txID int) bool
}
