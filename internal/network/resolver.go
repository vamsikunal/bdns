package network

import (
	"fmt"
	"time"
)

// ResolveDomain queries the BDNS blockchain for a domain
func ResolveDomain(domain string, n *Node) (string, error) {
	// 1. Check local cache
	if ip, found := GetFromCache(domain); found {
		return ip, nil
	}

	// 2. Full nodes check local blockchain state
	if n.IsFullNode {
		n.TxMutex.Lock()
		tx := n.IndexManager.GetIP(domain)
		n.TxMutex.Unlock()

		if tx != nil {
			SetToCache(domain, tx.IP)
			return tx.IP, nil
		}
		return "", fmt.Errorf("domain %s not found in B-DNS", domain)
	}

	// 3. Light node: Ask a full node for the answer + proof
	query := DNSQueryMsg{DomainName: domain}
	for _, peerID := range n.KnownFullPeers {
		n.P2PNetwork.DirectMessage(MsgDNSQuery, query, peerID)
		break // Ask the first known full peer
	}

	// Wait for HandleDNSProof to cache the verified result
	time.Sleep(3 * time.Second)

	if ip, found := GetFromCache(domain); found {
		return ip, nil
	}

	// 4. Not found in BDNS
	return "", fmt.Errorf("domain %s not found in B-DNS", domain)
}
