package network

import (
	"fmt"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
)

// cnameDepthLimit is the maximum CNAME redirects before returning a loop error.
const cnameDepthLimit = 10

// ResolveDomain queries the BDNS blockchain for a domain.
func ResolveDomain(domain string, queryType string, n *Node, currentSlot int64, slotsPerDay int64) ([]blockchain.Record, error) {
	// 1. Check local cache
	if records, found := GetFromCache(domain, queryType); found {
		return records, nil
	}

	// 2. Full nodes check local blockchain state
	if n.IsFullNode {
		n.TxMutex.Lock()
		tx := n.IndexManager.GetDomain(domain)
		n.TxMutex.Unlock()

		if tx != nil {
			phase := blockchain.GetDomainPhase(currentSlot, tx.ExpirySlot, slotsPerDay)
			if phase != "active" {
				return nil, fmt.Errorf("domain %s is in %s phase", domain, phase)
			}

			// Follow CNAME chain only when the caller is NOT explicitly asking
			if queryType != "CNAME" && hasCNAME(tx.Records) {
				return resolveCNAME(domain, queryType, n, currentSlot, slotsPerDay, make(map[string]bool))
			}

			filtered := filterByType(tx.Records, queryType)
			if len(filtered) == 0 {
				return nil, fmt.Errorf("no %s records for domain %s", queryType, domain)
			}

			SetToCache(domain, queryType, filtered)
			return filtered, nil
		}
		return nil, fmt.Errorf("domain %s not found in B-DNS", domain)
	}

	// 3. Light node: Ask a full node for the answer + proof
	query := DNSQueryMsg{DomainName: domain}
	for _, peerID := range n.KnownFullPeers {
		n.P2PNetwork.DirectMessage(MsgDNSQuery, query, peerID)
		break // Ask the first known full peer
	}

	// Wait for HandleDNSProof to cache the verified result
	time.Sleep(3 * time.Second)

	if records, found := GetFromCache(domain, queryType); found {
		return records, nil
	}

	// 4. Not found in BDNS
	return nil, fmt.Errorf("domain %s not found in B-DNS", domain)
}

// filterByType returns all records of the requested DNS type.
func filterByType(records []blockchain.Record, queryType string) []blockchain.Record {
	var result []blockchain.Record
	for _, r := range records {
		if r.Type == queryType {
			result = append(result, r)
		}
	}
	return result
}

// hasCNAME returns true if the record set contains at least one CNAME record.
func hasCNAME(records []blockchain.Record) bool {
	for _, r := range records {
		if r.Type == "CNAME" {
			return true
		}
	}
	return false
}

// resolveCNAME follows a CNAME chain recursively with visited-set loop protection.
func resolveCNAME(domain string, queryType string, n *Node, currentSlot int64, slotsPerDay int64, visited map[string]bool) ([]blockchain.Record, error) {
	if visited[domain] {
		return nil, fmt.Errorf("CNAME loop detected involving domain %s", domain)
	}
	if len(visited) >= cnameDepthLimit {
		return nil, fmt.Errorf("CNAME chain exceeded depth limit of %d at domain %s", cnameDepthLimit, domain)
	}

	visited[domain] = true

	n.TxMutex.Lock()
	tx := n.IndexManager.GetDomain(domain)
	n.TxMutex.Unlock()

	if tx == nil {
		return nil, fmt.Errorf("domain %s not found in B-DNS (CNAME chain)", domain)
	}

	phase := blockchain.GetDomainPhase(currentSlot, tx.ExpirySlot, slotsPerDay)
	if phase != "active" {
		return nil, fmt.Errorf("domain %s is in %s phase (CNAME chain)", domain, phase)
	}

	// Follow the CNAME to its target, or filter by requested type if no CNAME here.
	cnameRecords := filterByType(tx.Records, "CNAME")
	if len(cnameRecords) == 0 {
		filtered := filterByType(tx.Records, queryType)
		if len(filtered) == 0 {
			return nil, fmt.Errorf("no %s records for domain %s (CNAME chain)", queryType, domain)
		}
		return filtered, nil
	}

	return resolveCNAME(cnameRecords[0].Value, queryType, n, currentSlot, slotsPerDay, visited)
}
