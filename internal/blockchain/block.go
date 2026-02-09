package blockchain

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"log"
	"math/big"
	"sort"
	"time"
)

type Block struct {
	Index          int64
	Timestamp      int64  // Not Required
	SlotNumber     int64  // Discrete slot identifier for deterministic timing
	SlotLeader     []byte
	Signature      []byte
	IndexHash      []byte
	MerkleRootHash []byte
	StakeData      map[string]int // Registry Public Key -> Stake
	Transactions   []Transaction
	PrevHash       []byte
	Hash           []byte
}

func NewBlock(index int64, slotNumber int64, slotLeader []byte, indexHash []byte, transactions []Transaction, prevHash []byte, prevStakeData map[string]int, privateKey *ecdsa.PrivateKey) *Block {
	block := &Block{
		Index:        index,
		Timestamp:    time.Now().Unix(),
		SlotNumber:   slotNumber, // Set slot number
		SlotLeader:   slotLeader,
		IndexHash:    indexHash,
		Transactions: transactions,
		PrevHash:     prevHash,
	}

	block.MerkleRootHash = block.SetupMerkleTree()
	block.StakeData = block.SetupStakeData(prevStakeData)
	block.Signature = block.SignBlock(privateKey)
	block.Hash = block.ComputeHash()

	return block
}

func (b *Block) SetupStakeData(prevStakeData map[string]int) map[string]int {
	stakeData := make(map[string]int)
	// Copy previous stake data
	for key, value := range prevStakeData {
		stakeData[key] = value
	}

	// Update stake data based on transactions
	for _, tx := range b.Transactions {
		if tx.Type == REGISTER {
			stakeData[hex.EncodeToString(tx.OwnerKey)]++ // Increase stake for new domain registration
		} else if tx.Type == REVOKE {
			stakeData[hex.EncodeToString(tx.OwnerKey)]-- // Decrease stake for revoked domain
		}
	}

	return stakeData
}

func (b *Block) SignBlock(privateKey *ecdsa.PrivateKey) []byte {
	blockData := b.SerializeForSigning()
	hash := sha256.Sum256(blockData)

	r, s, err := ecdsa.Sign(rand.Reader, privateKey, hash[:])
	if err != nil {
		log.Panic("Failed to sign block:", err)
		return nil
	}

	// Ensure r and s are exactly 32 bytes each
	rBytes := r.Bytes()
	sBytes := s.Bytes()

	// ECDSA signatures are (r, s), each 32 bytes. Pad to ensure fixed size.
	rPadded := make([]byte, 32)
	sPadded := make([]byte, 32)
	copy(rPadded[32-len(rBytes):], rBytes)
	copy(sPadded[32-len(sBytes):], sBytes)

	// Concatenate r and s
	signature := append(rPadded, sPadded...)
	b.Signature = signature

	return signature
}

func (b *Block) VerifyBlock(publicKeyBytes []byte) bool {
	publicKey, err := BytesToPublicKey(publicKeyBytes)
	if err != nil {
		log.Println("Invalid public key format")
		return false
	}

	blockData := b.SerializeForSigning()
	hash := sha256.Sum256(blockData)

	if len(b.Signature) != 64 {
		log.Println("Invalid signature length")
		return false
	}

	r := new(big.Int).SetBytes(b.Signature[:32])
	s := new(big.Int).SetBytes(b.Signature[32:])

	return ecdsa.Verify(publicKey, hash[:], r, s)
}

func (b *Block) GetStakeDataBytes() []byte {
	// Extract keys from the map
	keys := make([]string, 0, len(b.StakeData))
	for key := range b.StakeData {
		keys = append(keys, key)
	}

	sort.Strings(keys) // Sort keys to ensure deterministic order

	stakeDataBytes := []byte{}
	for _, key := range keys {
		value := b.StakeData[key]
		decodedKey, _ := hex.DecodeString(key)
		stakeDataBytes = append(stakeDataBytes, decodedKey...)
		stakeDataBytes = append(stakeDataBytes, IntToByteArr(int64(value))...)
	}

	return stakeDataBytes
}

// Same as ComputeHash, but ommits Signature field
func (b *Block) SerializeForSigning() []byte {
	stakeDataBytes := b.GetStakeDataBytes()

	data := bytes.Join(
		[][]byte{
			IntToByteArr(b.Index),
			IntToByteArr(b.Timestamp),
			IntToByteArr(b.SlotNumber), // Include SlotNumber
			b.SlotLeader,
			b.IndexHash,
			b.MerkleRootHash,
			stakeDataBytes,
			b.PrevHash,
		},
		[]byte{},
	)

	hash := sha256.Sum256(data)
	return hash[:]
}

func NewGenesisBlock(slotLeader []byte, privateKey *ecdsa.PrivateKey, registryKeys [][]byte, randomness []byte) *Block {
	stakeData := make(map[string]int)
	n := len(registryKeys)
	if n == 0 {
		log.Panic("No registries provided for genesis block")
	}

	// Initialize stakes to zero
	for _, key := range registryKeys {
		stakeData[hex.EncodeToString(key)] = 0
	}

	genesisBlock := Block{
		Index:          0,
		Timestamp:      time.Now().Unix(),
		SlotNumber:     0,
		SlotLeader:     slotLeader,
		Signature:      []byte{},
		IndexHash:      []byte{},
		MerkleRootHash: []byte{},
		StakeData:      stakeData,
		Transactions:   []Transaction{},
		PrevHash:       randomness, // Storing randomness in PrevHash field
		Hash:           []byte{},
	}

	genesisBlock.Signature = genesisBlock.SignBlock(privateKey)
	genesisBlock.Hash = genesisBlock.ComputeHash()

	return &genesisBlock
}

func (b *Block) ComputeHash() []byte {
	stakeDataBytes := b.GetStakeDataBytes()

	data := bytes.Join(
		[][]byte{
			IntToByteArr(b.Index),
			IntToByteArr(b.Timestamp),
			IntToByteArr(b.SlotNumber),
			b.SlotLeader,
			b.Signature,
			b.IndexHash,
			b.MerkleRootHash,
			stakeDataBytes,
			b.PrevHash,
		},
		[]byte{},
	)

	hash := sha256.Sum256(data)
	return hash[:]
}

// Creates a merkle tree from the block's transactions and returns the root hash
func (b *Block) SetupMerkleTree() []byte {
	var transactions [][]byte

	for _, tx := range b.Transactions {
		transactions = append(transactions, tx.Serialize())
	}
	mTree := NewMerkleTree(transactions)

	return mTree.RootNode.Data
}

func (b *Block) Serialize() []byte {
	var result bytes.Buffer
	encoder := gob.NewEncoder(&result)

	err := encoder.Encode(b)
	if err != nil {
		log.Panic(err)
	}

	return result.Bytes()
}

func DeserializeBlock(d []byte) *Block {
	var block Block

	decoder := gob.NewDecoder(bytes.NewReader(d))
	err := decoder.Decode(&block)
	if err != nil {
		log.Panic(err)
	}

	return &block
}

func ValidateGenesisBlock(block *Block, registryKeys [][]byte, slotLeaderKey []byte) bool {
	if block.Index != 0 {
		return false
	}

	if !bytes.Equal(block.SlotLeader, slotLeaderKey) {
		return false
	}

	if !block.VerifyBlock(slotLeaderKey) {
		return false
	}

	if len(block.StakeData) != len(registryKeys) {
		return false
	}

	for _, key := range registryKeys {
		if block.StakeData[hex.EncodeToString(key)] != 0 {
			return false
		}
	}

	if len(block.Transactions) != 0 {
		return false
	}

	if !bytes.Equal(block.Hash, block.ComputeHash()) {
		return false
	}

	return true
}

func ValidateBlock(newBlock *Block, oldBlock *Block, slotLeaderKey []byte, expiryChecker ExpiryChecker) bool {
	if oldBlock.Index+1 != newBlock.Index {
		return false
	}

	if newBlock.SlotNumber <= oldBlock.SlotNumber {
		return false
	}

	if !bytes.Equal(oldBlock.Hash, newBlock.PrevHash) {
		return false
	}

	if !bytes.Equal(newBlock.SlotLeader, slotLeaderKey) {
		return false
	}

	if !newBlock.VerifyBlock(slotLeaderKey) {
		return false
	}

	if !bytes.Equal(newBlock.MerkleRootHash, newBlock.SetupMerkleTree()) {
		return false
	}

	if !areStakesEqual(newBlock.StakeData, newBlock.SetupStakeData(oldBlock.StakeData)) {
		return false
	}

	if !newBlock.ValidateTransactions() {
		return false
	}

	if !bytes.Equal(newBlock.Hash, newBlock.ComputeHash()) {
		return false
	}

	return true
}

func (b *Block) ValidateTransactions() bool {
	for _, tx := range b.Transactions {
		if !VerifyTransaction(tx.OwnerKey, &tx) {
			return false
		}
	}
	return true
}

// MerkleProof contains the data needed for a light node to verify transaction inclusion
type MerkleProof struct {
	TxHash     []byte
	ProofPath  [][]byte
	Directions []bool // true = right sibling, false = left sibling
	MerkleRoot []byte
}

// BlockHeader is a lightweight representation for light nodes
type BlockHeader struct {
	Index      int64
	SlotNumber int64
	Hash       []byte
	PrevHash   []byte
	MerkleRoot []byte
	IndexHash  []byte
}

// State returns the state st_j = H(B_{j-1})
func (b *Block) State() []byte {
	return b.PrevHash
}

// extracts a lightweight header from a full block
func (b *Block) Header() BlockHeader {
	return BlockHeader{
		Index:      b.Index,
		SlotNumber: b.SlotNumber,
		Hash:       b.Hash,
		PrevHash:   b.PrevHash,
		MerkleRoot: b.MerkleRootHash,
		IndexHash:  b.IndexHash,
	}
}

// GenerateMerkleProof creates a compact Merkle proof for a transaction at the given index
func (b *Block) GenerateMerkleProof(txIndex int) *MerkleProof {
	if txIndex < 0 || txIndex >= len(b.Transactions) {
		return nil
	}

	// Build leaf hashes
	var leaves [][]byte
	for _, tx := range b.Transactions {
		txBytes := tx.Serialize()
		hash := sha256.Sum256(txBytes)
		leaves = append(leaves, hash[:])
	}

	txHash := make([]byte, len(leaves[txIndex]))
	copy(txHash, leaves[txIndex])

	// Build proof path from leaf to root
	var proofPath [][]byte
	var directions []bool
	index := txIndex
	level := leaves

	for len(level) > 1 {
		// Pad if odd number of nodes
		if len(level)%2 != 0 {
			dup := make([]byte, len(level[len(level)-1]))
			copy(dup, level[len(level)-1])
			level = append(level, dup)
		}

		// Record sibling hash
		if index%2 == 0 {
			sibling := make([]byte, len(level[index+1]))
			copy(sibling, level[index+1])
			proofPath = append(proofPath, sibling)
			directions = append(directions, true) // sibling on right
		} else {
			sibling := make([]byte, len(level[index-1]))
			copy(sibling, level[index-1])
			proofPath = append(proofPath, sibling)
			directions = append(directions, false) // sibling on left
		}

		// Build next level
		var nextLevel [][]byte
		for i := 0; i < len(level); i += 2 {
			combined := append(level[i], level[i+1]...)
			hash := sha256.Sum256(combined)
			h := make([]byte, len(hash))
			copy(h, hash[:])
			nextLevel = append(nextLevel, h)
		}

		level = nextLevel
		index = index / 2
	}

	return &MerkleProof{
		TxHash:     txHash,
		ProofPath:  proofPath,
		Directions: directions,
		MerkleRoot: level[0],
	}
}

// VerifyMerkleProof verifies that a Merkle proof is valid
func VerifyMerkleProof(proof *MerkleProof) bool {
	if proof == nil {
		return false
	}

	currentHash := make([]byte, len(proof.TxHash))
	copy(currentHash, proof.TxHash)

	for i, sibling := range proof.ProofPath {
		var combined []byte
		if proof.Directions[i] {
			// sibling is on right
			combined = append(currentHash, sibling...)
		} else {
			// sibling is on left
			combined = append(sibling, currentHash...)
		}
		hash := sha256.Sum256(combined)
		currentHash = hash[:]
	}

	return bytes.Equal(currentHash, proof.MerkleRoot)
}
