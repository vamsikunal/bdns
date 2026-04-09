package network

import (
	"log"
	"net"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/miekg/dns"
)

// RFC1035Handler translates blockchain DNS records into binary RFC 1035 responses.
type RFC1035Handler struct {
	node             *Node
	slotInterval     int64
	initialTimestamp int64
}

// NewRFC1035Handler constructs a handler bound to the given node.
func NewRFC1035Handler(n *Node) *RFC1035Handler {
	return &RFC1035Handler{
		node:             n,
		slotInterval:     n.Config.SlotInterval,
		initialTimestamp: n.Config.InitialTimestamp,
	}
}

// ServeDNS satisfies dns.Handler; calls HandleDNSQuery and writes the response.
func (h *RFC1035Handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	resp := h.HandleDNSQuery(r)
	if err := w.WriteMsg(resp); err != nil {
		log.Printf("[RFC1035] write error: %v", err)
	}
}

// HandleDNSQuery resolves a DNS query against the blockchain index.
func (h *RFC1035Handler) HandleDNSQuery(msg *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(msg)
	resp.Authoritative = true

	if len(msg.Question) == 0 {
		resp.Rcode = dns.RcodeNameError
		return resp
	}

	fqdn := msg.Question[0].Name
	qtype := msg.Question[0].Qtype

	// Strip trailing dot for index lookup
	lookup := fqdn
	if len(lookup) > 0 && lookup[len(lookup)-1] == '.' {
		lookup = lookup[:len(lookup)-1]
	}

	// Narrow critical section: lock only for the index read then release
	h.node.TxMutex.Lock()
	tx := h.node.IndexManager.GetDomain(lookup)
	if tx == nil {
		h.node.TxMutex.Unlock()
		resp.Rcode = dns.RcodeNameError
		return resp
	}
	records := make([]blockchain.Record, len(tx.Records))
	copy(records, tx.Records)
	expirySlot := tx.ExpirySlot
	cacheTTL := tx.CacheTTL
	h.node.TxMutex.Unlock()

	// Phase check outside the lock
	currentSlot := (time.Now().Unix() - h.initialTimestamp) / h.slotInterval
	slotsPerDay := int64(86400) / h.slotInterval
	phase := blockchain.GetDomainPhase(currentSlot, expirySlot, slotsPerDay)
	if phase != "active" {
		resp.Rcode = dns.RcodeNameError
		return resp
	}

	ttl := h.capTTL(uint32(cacheTTL))

	switch qtype {
	case dns.TypeA:
		resp.Answer = h.buildARecords(fqdn, records, ttl)
	case dns.TypeAAAA:
		resp.Answer = h.buildAAAARecords(fqdn, records, ttl)
	case dns.TypeCNAME:
		resp.Answer = h.buildCNAMERecords(fqdn, records, ttl)
	case dns.TypeMX:
		resp.Answer = h.buildMXRecords(fqdn, records, ttl)
	case dns.TypeTXT:
		resp.Answer = h.buildTXTRecords(fqdn, records, ttl)
	case dns.TypeNS:
		resp.Answer = h.buildNSRecords(fqdn, records, ttl)
	default:
		resp.Answer = h.buildAllRecords(fqdn, records, ttl)
	}

	if len(resp.Answer) == 0 {
		resp.Rcode = dns.RcodeNameError
	}
	return resp
}

// capTTL enforces min(ttl, slotInterval); zero defaults to slotInterval.
func (h *RFC1035Handler) capTTL(ttl uint32) uint32 {
	cap := uint32(h.slotInterval)
	if ttl == 0 || ttl > cap {
		return cap
	}
	return ttl
}

func (h *RFC1035Handler) buildARecords(name string, records []blockchain.Record, ttl uint32) []dns.RR {
	var rrs []dns.RR
	for _, r := range records {
		if r.Type != "A" {
			continue
		}
		ip := net.ParseIP(r.Value).To4()
		if ip == nil {
			continue
		}
		rrs = append(rrs, &dns.A{
			Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
			A:   ip,
		})
	}
	return rrs
}

func (h *RFC1035Handler) buildAAAARecords(name string, records []blockchain.Record, ttl uint32) []dns.RR {
	var rrs []dns.RR
	for _, r := range records {
		if r.Type != "AAAA" {
			continue
		}
		ip := net.ParseIP(r.Value)
		if ip == nil {
			continue
		}
		rrs = append(rrs, &dns.AAAA{
			Hdr:  dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: ttl},
			AAAA: ip,
		})
	}
	return rrs
}

func (h *RFC1035Handler) buildCNAMERecords(name string, records []blockchain.Record, ttl uint32) []dns.RR {
	var rrs []dns.RR
	for _, r := range records {
		if r.Type != "CNAME" {
			continue
		}
		rrs = append(rrs, &dns.CNAME{
			Hdr:    dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: ttl},
			Target: dns.Fqdn(r.Value),
		})
	}
	return rrs
}

func (h *RFC1035Handler) buildMXRecords(name string, records []blockchain.Record, ttl uint32) []dns.RR {
	var rrs []dns.RR
	for _, r := range records {
		if r.Type != "MX" {
			continue
		}
		rrs = append(rrs, &dns.MX{
			Hdr:        dns.RR_Header{Name: name, Rrtype: dns.TypeMX, Class: dns.ClassINET, Ttl: ttl},
			Preference: uint16(r.Priority),
			Mx:         dns.Fqdn(r.Value),
		})
	}
	return rrs
}

func (h *RFC1035Handler) buildTXTRecords(name string, records []blockchain.Record, ttl uint32) []dns.RR {
	var rrs []dns.RR
	for _, r := range records {
		if r.Type != "TXT" {
			continue
		}
		rrs = append(rrs, &dns.TXT{
			Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: ttl},
			Txt: []string{r.Value},
		})
	}
	return rrs
}

func (h *RFC1035Handler) buildNSRecords(name string, records []blockchain.Record, ttl uint32) []dns.RR {
	var rrs []dns.RR
	for _, r := range records {
		if r.Type != "NS" {
			continue
		}
		rrs = append(rrs, &dns.NS{
			Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: ttl},
			Ns:  dns.Fqdn(r.Value),
		})
	}
	return rrs
}

func (h *RFC1035Handler) buildAllRecords(name string, records []blockchain.Record, ttl uint32) []dns.RR {
	var rrs []dns.RR
	rrs = append(rrs, h.buildARecords(name, records, ttl)...)
	rrs = append(rrs, h.buildAAAARecords(name, records, ttl)...)
	rrs = append(rrs, h.buildCNAMERecords(name, records, ttl)...)
	rrs = append(rrs, h.buildMXRecords(name, records, ttl)...)
	rrs = append(rrs, h.buildTXTRecords(name, records, ttl)...)
	rrs = append(rrs, h.buildNSRecords(name, records, ttl)...)
	return rrs
}

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
