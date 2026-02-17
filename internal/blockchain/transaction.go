package blockchain

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"log"
	"math/big"
	"time"
)

type TransactionType uint8

const (
	REGISTER TransactionType = iota
	UPDATE
	REVOKE
	RENEW
)

type Transaction struct {
	TID         int
	Type        TransactionType
	Timestamp   int64
	DomainName  string
	IP          string
	CacheTTL    int64  // How long resolvers should cache (seconds)
	ExpirySlot  int64  // Slot number when domain registration expires
	RedeemsTxID int    // For UPDATE/REVOKE - references previous tx (0 for REGISTER)
	OwnerKey    []byte
	Signature   []byte
}

func NewTransaction(txType TransactionType, domainName, ip string, cacheTTL int64,
	currentSlot int64, slotsPerDay int64, redeemsTxID int,
	ownerKey []byte, privateKey *ecdsa.PrivateKey, txPool map[int]*Transaction) *Transaction {
	
	tx := Transaction{
		TID:         GenerateRandomTxID(txPool),
		Type:        txType,
		Timestamp:   time.Now().Unix(),
		DomainName:  domainName,
		IP:          ip,
		CacheTTL:    cacheTTL,
		RedeemsTxID: redeemsTxID, // transaction ID being redeemed (0 for REGISTER, required for UPDATE/REVOKE)
		OwnerKey:    ownerKey,
		Signature:   nil,
	}

	// Calculate expiry for REGISTER (1 year = 365 * slotsPerDay (number of slots per day (for expiry calculation)))
	if txType == REGISTER {
		// currentSlot: current slot number (for calculating ExpirySlot)
		// TODO: It can be made variable, but for now it is fixed.
		tx.ExpirySlot = currentSlot + (365 * slotsPerDay) // Default 1 year validity
		tx.RedeemsTxID = 0                                // REGISTER doesn't redeem anything
	}

	tx.Signature = SignTransaction(privateKey, &tx)
	return &tx
}

// NewRenewTransaction creates a RENEW transaction for an existing domain.
func NewRenewTransaction(domainName, ip string, cacheTTL int64,
	oldExpirySlot int64, slotsPerDay int64, redeemsTxID int,
	ownerKey []byte, registryPrivKey *ecdsa.PrivateKey, txPool map[int]*Transaction) *Transaction {

	tx := Transaction{
		TID:         GenerateRandomTxID(txPool),
		Type:        RENEW,
		Timestamp:   time.Now().Unix(),
		DomainName:  domainName,
		IP:          ip,
		CacheTTL:    cacheTTL,
		ExpirySlot:  oldExpirySlot + (365 * slotsPerDay), // Extend from old expiry, not current slot
		RedeemsTxID: redeemsTxID,
		OwnerKey:    ownerKey,
		Signature:   nil,
	}
	tx.Signature = SignTransaction(registryPrivKey, &tx)
	return &tx
}

func SignTransaction(privateKey *ecdsa.PrivateKey, tx *Transaction) []byte {
	txData := tx.SerializeForSigning()
	hash := sha256.Sum256(txData)

	r, s, err := ecdsa.Sign(rand.Reader, privateKey, hash[:])
	if err != nil {
		log.Panic("Failed to sign transaction:", err)
		return nil
	}

	// Ensure r and s are exactly 32 bytes each
	rBytes := r.Bytes()
	sBytes := s.Bytes()

	// Pad r and s to fixed 32-byte size
	rPadded := make([]byte, 32)
	sPadded := make([]byte, 32)
	copy(rPadded[32-len(rBytes):], rBytes)
	copy(sPadded[32-len(sBytes):], sBytes)

	// Concatenate r and s
	signature := append(rPadded, sPadded...)
	tx.Signature = signature

	return signature
}

// Function to verify the ECDSA signature on a transaction
func VerifySignature(publicKeyBytes []byte, tx *Transaction) bool {
	publicKey, err := BytesToPublicKey(publicKeyBytes)
	if err != nil {
		log.Println("Invalid public key format")
		return false
	}

	txData := tx.SerializeForSigning()
	hash := sha256.Sum256(txData)

	// Ensure signature length is correct
	if len(tx.Signature) != 64 {
		log.Println("Invalid signature length")
		return false
	}

	// Extract r and s from the signature
	r := new(big.Int).SetBytes(tx.Signature[:32])
	s := new(big.Int).SetBytes(tx.Signature[32:])

	return ecdsa.Verify(publicKey, hash[:], r, s)
}

// VerifyTransaction validates a transaction based on its type
func VerifyTransaction(publicKeyBytes []byte, tx *Transaction, currentSlot int64, slotsPerDay int64) bool {
	switch tx.Type {
	case REGISTER:
		if !IsRegistryKey(tx.OwnerKey) {
			log.Println("REGISTER not signed by trusted registry")
			return false
		}
		return VerifySignature(tx.OwnerKey, tx)

	case UPDATE:
		return VerifySignature(publicKeyBytes, tx)

	case REVOKE:
		// System transaction (auto-revocation) have no signature and are validated by expiry check
		if tx.Signature == nil && tx.OwnerKey == nil {
			purgeSlot := ComputePurgeSlot(tx.ExpirySlot, slotsPerDay)
			if purgeSlot <= currentSlot {
				return true
			}
			log.Println("Invalid auto-revocation: domain not past purge slot")
			return false
		}
		// Manual revocation
		return VerifySignature(publicKeyBytes, tx)

	case RENEW:
		if !VerifyRegistrySignature(tx) {
			log.Println("RENEW not signed by any trusted registry")
			return false
		}
		return true
	}

	return false
}

// VerifyRegistrySignature checks if any trusted registry signed the transaction
func VerifyRegistrySignature(tx *Transaction) bool {
	for _, regKey := range TrustedRegistries {
		if VerifySignature(regKey, tx) {
			return true
		}
	}
	return false
}

// ValidateRedemption ensures UPDATE/REVOKE/RENEW transactions properly redeem a previous transaction
func ValidateRedemption(tx *Transaction, blockchain *Blockchain, currentSlot int64, slotsPerDay int64) bool {
	if tx.Type == REGISTER {
		return true
	}

	if tx.RedeemsTxID == 0 {
		log.Println("UPDATE/REVOKE/RENEW must redeem a previous transaction")
		return false
	}

	// Check if already redeemed (double-spend prevention)
	if blockchain.IsSpent(tx.RedeemsTxID) {
		log.Println("Transaction already redeemed:", tx.RedeemsTxID)
		return false
	}

	// Find the redeemed transaction
	prevTx, err := blockchain.FindTransaction(tx.RedeemsTxID)
	if err != nil {
		log.Println("Redeemed transaction not found:", tx.RedeemsTxID)
		return false
	}

	// Verify domain name matches
	if prevTx.DomainName != tx.DomainName {
		log.Println("Domain name mismatch in redemption")
		return false
	}

	// Verify ownership
	if !bytes.Equal(prevTx.OwnerKey, tx.OwnerKey) {
		log.Println("Owner key mismatch in redemption")
		return false
	}

	// Phase-specific validation
	phase := GetDomainPhase(currentSlot, prevTx.ExpirySlot, slotsPerDay)

	switch tx.Type {
	case UPDATE:
		if phase != "active" {
			log.Println("UPDATE rejected: domain is in", phase, "phase (must be active)")
			return false
		}

	case RENEW:
		if phase == "purged" {
			log.Println("RENEW rejected: domain is purged (grace period has ended)")
			return false
		}

	case REVOKE:
		if phase == "purged" && tx.Signature != nil {
			log.Println("Manual REVOKE rejected: domain already purged")
			return false
		}
	}

	return true
}

func (tx *Transaction) SerializeForSigning() []byte {
	txData := append(IntToByteArr(int64(tx.TID)), byte(tx.Type))
	txData = append(txData, IntToByteArr(tx.Timestamp)...)
	txData = append(txData, []byte(tx.DomainName)...)
	txData = append(txData, []byte(tx.IP)...)
	txData = append(txData, IntToByteArr(tx.CacheTTL)...)
	txData = append(txData, IntToByteArr(tx.ExpirySlot)...)
	txData = append(txData, IntToByteArr(int64(tx.RedeemsTxID))...)
	txData = append(txData, tx.OwnerKey...)

	return txData
}

func (tx *Transaction) Serialize() []byte {
	var result bytes.Buffer
	encoder := gob.NewEncoder(&result)

	err := encoder.Encode(tx)
	if err != nil {
		log.Panic(err)
	}

	return result.Bytes()
}

func DeserializeTx(d []byte) *Transaction {
	var transaction Transaction

	decoder := gob.NewDecoder(bytes.NewReader(d))
	err := decoder.Decode(&transaction)
	if err != nil {
		log.Panic(err)
	}

	return &transaction
}

// TODO: Check if TxID is among existing IDs, not just in the pool
// GenerateRandomTxID generates a unique random transaction ID using crypto/rand
func GenerateRandomTxID(txPool map[int]*Transaction) int {
	for {
		var buf [8]byte
		_, err := rand.Read(buf[:]) // Read 8 random bytes
		if err != nil {
			panic("Failed to generate random transaction ID")
		}

		txID := int(binary.LittleEndian.Uint64(buf[:]) % 1_000_000_000) // Ensure within range

		if _, exists := txPool[txID]; !exists { // Ensure uniqueness
			return txID
		}
	}
}

func RemoveTxsFromPool(txs []Transaction, txPool map[int]*Transaction) {
	for _, tx := range txs {
		delete(txPool, tx.TID)
	}
}
