package network

import (
	"log"

	"github.com/miekg/dns"
)

// StartDNSServer initialises and starts an RFC 1035-compliant UDP DNS server on the given port.
func StartDNSServer(port string, node *Node) {
	handler := NewRFC1035Handler(node)
	mux := dns.NewServeMux()
	mux.Handle(".", handler)

	srv := &dns.Server{
		Addr:    ":" + port,
		Net:     "udp",
		Handler: mux,
	}
	node.DNSServer = srv

	if err := srv.ListenAndServe(); err != nil {
		log.Printf("[DNS] UDP server on :%s stopped: %v", port, err)
	}
}
