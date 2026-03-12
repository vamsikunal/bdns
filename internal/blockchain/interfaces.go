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
	GetIndexHash() []byte
	MarkAsSpent(txID int)
	IsSpent(txID int) bool
	Commit()
}

// StakeStorer abstracts staked-balance operations for block validation.
type StakeStorer interface {
	AddStake(validatorHex string, amount uint64)
	ReduceStake(validatorHex string, amount uint64)
	GetStake(validatorHex string) uint64
	GetAll() map[string]uint64
	HasAnyStake() bool
	Hash() []byte
	Clone() StakeStorer
}

// CommitStorer abstracts the pending commits store for validation.
type CommitStorer interface {
	AddCommit(record *CommitRecord)
	GetCommit(commitHashHex string) *CommitRecord
	ConsumeCommit(commitHashHex string)
	PurgeExpired(currentBlockIndex int64) int
	Hash() []byte
}

