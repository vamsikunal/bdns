package network

import "encoding/json"

// MessageType defines various message types in the P2P network
type MessageType string

const (
	DNSRequest      MessageType = "DNS_REQUEST"
	DNSResponse     MessageType = "DNS_RESPONSE"
	MsgTransaction  MessageType = "TRANSACTION"
	MsgBlock        MessageType = "BLOCK"
	MsgRandomNumber MessageType = "RANDOM_NUMBER"
	MsgInv          MessageType = "INV"
	MsgGetBlock     MessageType = "GET_BLOCK"
	MsgGetData      MessageType = "GET_DATA"
	MsgDNSQuery     MessageType = "DNS_QUERY"
	MsgDNSProof     MessageType = "DNS_PROOF"
	MsgMerkleProof  MessageType = "MERKLE_PROOF"
	MsgCommitment   MessageType = "DRG_COMMITMENT"
	MsgReveal       MessageType = "DRG_REVEAL"
	MsgSlotSkip     MessageType = "SLOT_SKIP"
)

// BDNSRequest is the legacy DNS request payload sent over gossip.
type BDNSRequest struct {
	DomainName string
}

// BDNSResponse is the legacy DNS response payload sent over gossip.
type BDNSResponse struct {
	Timestamp  int64
	DomainName string
	IP         string
	CacheTTL   int64
	OwnerKey   []byte
	Signature  []byte
}

// CommitmentMsg is broadcast in the commitment phase of the DRG protocol.
type CommitmentMsg struct {
	Epoch      int64  `json:"epoch"`
	Commitment []byte `json:"commitment"`
	Sender     string `json:"sender"`
	Signature  []byte `json:"signature"`
}

// RevealMsg is broadcast in the reveal phase of the DRG protocol.
type RevealMsg struct {
	Epoch     int64  `json:"epoch"`
	U         []byte `json:"u"`
	R         []byte `json:"r"`
	Sender    string `json:"sender"`
	Signature []byte `json:"signature"`
}

// GossipEnvelope is the wire-format message broadcast over pubsub.
type GossipEnvelope struct {
	Type    MessageType
	Sender  string // Peer ID
	Content json.RawMessage
	Metrics *DNSMetrics
}
