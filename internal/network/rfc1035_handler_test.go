package network

import (
	"net"
	"testing"
	"time"

	"github.com/bleasey/bdns/internal/blockchain"
	"github.com/bleasey/bdns/internal/index"
	"github.com/miekg/dns"
)

const testSlotInterval = 60

// makeHandler builds a minimal Node + RFC1035Handler backed by a fresh IndexManager.
func makeHandler(t *testing.T) (*RFC1035Handler, *index.IndexManager) {
	t.Helper()
	im := index.NewIndexManager()
	node := &Node{
		IndexManager: im,
		Config: NodeConfig{
			SlotInterval:     testSlotInterval,
			InitialTimestamp: time.Now().Unix(),
		},
	}
	return NewRFC1035Handler(node), im
}

// putDomain inserts a domain transaction into im with the given expiry slot.
func putDomain(im *index.IndexManager, domain string, records []blockchain.Record, cacheTTL int64, expirySlot int64) {
	tx := &blockchain.Transaction{
		TID:        1,
		DomainName: domain,
		Records:    records,
		CacheTTL:   cacheTTL,
		ExpirySlot: expirySlot,
	}
	im.Add(domain, tx, 1, 0, 17280) // slotsPerDay = 86400/5
}

// dnsQ builds a minimal DNS question message.
func dnsQ(domain string, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	return m
}

func TestRFC1035Handler_ARecord(t *testing.T) {
	h, im := makeHandler(t)
	putDomain(im, "test.bdns", []blockchain.Record{{Type: "A", Value: "1.2.3.4"}}, 300, 1_000_000)

	resp := h.HandleDNSQuery(dnsQ("test.bdns", dns.TypeA))

	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected RcodeSuccess, got %d", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatal("answer is not an A record")
	}
	if !a.A.Equal(net.ParseIP("1.2.3.4")) {
		t.Errorf("expected 1.2.3.4, got %v", a.A)
	}
}

func TestRFC1035Handler_NXDOMAIN(t *testing.T) {
	h, _ := makeHandler(t)

	resp := h.HandleDNSQuery(dnsQ("notexist.bdns", dns.TypeA))

	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("expected RcodeNameError (NXDOMAIN), got %d", resp.Rcode)
	}
}

func TestRFC1035Handler_TTLCap(t *testing.T) {
	records := []blockchain.Record{{Type: "A", Value: "5.5.5.5"}}

	// Above cap: cacheTTL=1000 > slotInterval=60 → response TTL must be capped at 60.
	h1, im1 := makeHandler(t)
	putDomain(im1, "highttl.bdns", records, 1000, 1_000_000)
	resp := h1.HandleDNSQuery(dnsQ("highttl.bdns", dns.TypeA))
	if got := resp.Answer[0].Header().Ttl; got != testSlotInterval {
		t.Errorf("above-cap: expected TTL=%d, got %d", testSlotInterval, got)
	}

	// Below cap: cacheTTL=30 < slotInterval=60 → response TTL must be 30.
	h2, im2 := makeHandler(t)
	putDomain(im2, "lowttl.bdns", records, 30, 1_000_000)
	resp2 := h2.HandleDNSQuery(dnsQ("lowttl.bdns", dns.TypeA))
	if got := resp2.Answer[0].Header().Ttl; got != 30 {
		t.Errorf("below-cap: expected TTL=30, got %d", got)
	}

	// Zero TTL: cacheTTL=0 → defaults to slotInterval.
	h3, im3 := makeHandler(t)
	putDomain(im3, "zerttl.bdns", records, 0, 1_000_000)
	resp3 := h3.HandleDNSQuery(dnsQ("zerttl.bdns", dns.TypeA))
	if got := resp3.Answer[0].Header().Ttl; got != testSlotInterval {
		t.Errorf("zero-ttl: expected TTL=%d, got %d", testSlotInterval, got)
	}
}

func TestRFC1035Handler_ExpiredDomain(t *testing.T) {
	h, im := makeHandler(t)
	// expirySlot=0 → currentSlot(0) >= expirySlot → phase is "grace" or "purged", not "active".
	putDomain(im, "expired.bdns", []blockchain.Record{{Type: "A", Value: "9.9.9.9"}}, 300, 0)

	resp := h.HandleDNSQuery(dnsQ("expired.bdns", dns.TypeA))

	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("expected NXDOMAIN for expired domain, got rcode=%d", resp.Rcode)
	}
}

func TestRFC1035Handler_AllRecordTypes(t *testing.T) {
	h, im := makeHandler(t)
	putDomain(im, "multi.bdns", []blockchain.Record{
		{Type: "MX", Value: "mail.multi.bdns", Priority: 10},
		{Type: "TXT", Value: "v=spf1 -all"},
		{Type: "AAAA", Value: "::1"},
		{Type: "CNAME", Value: "alias.multi.bdns"},
		{Type: "NS", Value: "ns1.multi.bdns"},
	}, 60, 1_000_000)

	cases := []struct {
		qtype    uint16
		wantType uint16
	}{
		{dns.TypeMX, dns.TypeMX},
		{dns.TypeTXT, dns.TypeTXT},
		{dns.TypeAAAA, dns.TypeAAAA},
		{dns.TypeCNAME, dns.TypeCNAME},
		{dns.TypeNS, dns.TypeNS},
	}

	for _, c := range cases {
		resp := h.HandleDNSQuery(dnsQ("multi.bdns", c.qtype))
		if resp.Rcode != dns.RcodeSuccess {
			t.Errorf("qtype=%d: expected RcodeSuccess, got %d", c.qtype, resp.Rcode)
			continue
		}
		if len(resp.Answer) == 0 {
			t.Errorf("qtype=%d: expected at least 1 answer", c.qtype)
			continue
		}
		if got := resp.Answer[0].Header().Rrtype; got != c.wantType {
			t.Errorf("qtype=%d: expected Rrtype=%d, got %d", c.qtype, c.wantType, got)
		}
	}
}
