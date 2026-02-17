package blockchain

// ExpiryChecker provides expiration data for block validation
// Implemented by index.IndexManager to avoid circular dependency between packages
type ExpiryChecker interface {
	GetExpiredDomains(slotNumber int64) []*Transaction
	GetPurgeableDomains(slotNumber int64) []*Transaction
}
