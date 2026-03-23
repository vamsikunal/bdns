package network

import "encoding/json"

// MessageType defines various message types in the P2P network
type MessageType string

const (
	DNSRequest       MessageType = "DNS_REQUEST"
	DNSResponse      MessageType = "DNS_RESPONSE"
	MsgTransaction   MessageType = "TRANSACTION"
	MsgBlock         MessageType = "BLOCK"
	MsgChainRequest  MessageType = "CHAIN_REQUEST"
	MsgChainResponse MessageType = "CHAIN_RESPONSE"
	MsgPeerRequest   MessageType = "PEER_REQUEST"
	MsgPeerResponse  MessageType = "PEER_RESPONSE"
	MsgRandomNumber  MessageType = "RANDOM_NUMBER"
	MsgInv           MessageType = "INV"
	MsgGetBlock      MessageType = "GET_BLOCK"
	MsgGetData       MessageType = "GET_DATA"
	MsgGetMerkle     MessageType = "GET_MERKLE"
	MsgDNSQuery      MessageType = "DNS_QUERY"
	MsgDNSProof      MessageType = "DNS_PROOF"
	MsgMerkleProof   MessageType = "MERKLE_PROOF"
	MsgCommitment    MessageType = "DRG_COMMITMENT"
	MsgReveal        MessageType = "DRG_REVEAL"
)

// Message represents a generic network message
type Message struct {
	Sender string      `json:"sender"`
	Type   MessageType `json:"type"`
	Data   []byte      `json:"data"`
}

// Encode message to JSON
func (m *Message) Encode() []byte {
	data, _ := json.Marshal(m)
	return data
}

// Decode message from JSON
func DecodeMessage(data []byte) (*Message, error) {
	var msg Message
	err := json.Unmarshal(data, &msg)
	return &msg, err
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
