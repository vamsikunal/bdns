package network

import (
	"sync"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
)

// An "A" and "MX" query for the same domain are different cache entries.
type cacheKey struct {
	Domain    string
	QueryType string
}

type cacheEntry struct {
	records []blockchain.Record
	expiry  time.Time
}

var (
	cache      = make(map[cacheKey]cacheEntry)
	cacheMutex sync.Mutex
)

// GetFromCache retrieves cached records for a (domain, queryType) pair.
func GetFromCache(domain, queryType string) ([]blockchain.Record, bool) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	key := cacheKey{Domain: domain, QueryType: queryType}
	entry, found := cache[key]
	if !found || time.Now().After(entry.expiry) {
		delete(cache, key)
		return nil, false
	}
	return entry.records, true
}

// SetToCache stores records for a (domain, queryType) pair with a 300-second TTL.
func SetToCache(domain, queryType string, records []blockchain.Record) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	cache[cacheKey{Domain: domain, QueryType: queryType}] = cacheEntry{
		records: records,
		expiry:  time.Now().Add(300 * time.Second),
	}
}
