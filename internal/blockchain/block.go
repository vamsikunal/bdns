package blockchain

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"time"
)

type Block struct {
	Index              int64
	Timestamp          int64  // Not Required
	SlotNumber         int64  // Discrete slot identifier for deterministic timing
	SlotLeader         []byte
	Signature          []byte
	IndexHash          []byte
	BalanceLedgerHash  []byte
	MerkleRootHash     []byte
	StakeData          map[string]uint64 // Registry Public Key -> Stake (snapshot for legacy compat)
	Transactions       []Transaction
	PrevHash           []byte
	Hash               []byte
	CommitStoreHash  []byte
	StakeMapHash     []byte
	UnstakeQueueHash []byte
	DRGSeed          float64
}


func NewBlock(index int64, slotNumber int64, slotLeader []byte, indexHash []byte, balanceLedgerHash []byte,
	commitStoreHash []byte, transactions []Transaction, prevHash []byte,
	stakeMapHash []byte, unstakeQueueHash []byte, stakeData map[string]uint64,
	privateKey *ecdsa.PrivateKey) *Block {

	block := &Block{
		Index:             index,
		Timestamp:         time.Now().Unix(),
		SlotNumber:        slotNumber,
		SlotLeader:        slotLeader,
		IndexHash:         indexHash,
		BalanceLedgerHash: balanceLedgerHash,
		CommitStoreHash:   commitStoreHash,
		Transactions:      transactions,
		PrevHash:          prevHash,
		StakeMapHash:      stakeMapHash,
		UnstakeQueueHash:  unstakeQueueHash,
		StakeData:         stakeData,
	}

	block.MerkleRootHash = block.SetupMerkleTree()
	block.Signature = block.SignBlock(privateKey)
	block.Hash = block.ComputeHash()

	return block
}

// GetStakeSnapshot returns the current staked balances from sm as a plain map.
func (b *Block) GetStakeSnapshot(sm StakeStorer) map[string]uint64 {
	return sm.GetAll()
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


// Same as ComputeHash, but omits the Signature field
func (b *Block) SerializeForSigning() []byte {
	data := bytes.Join(
		[][]byte{
			IntToByteArr(b.Index),
			IntToByteArr(b.Timestamp),
			IntToByteArr(b.SlotNumber),
			b.SlotLeader,
			b.IndexHash,
			b.MerkleRootHash,
			b.StakeMapHash,
			b.UnstakeQueueHash,
			b.PrevHash,
		},
		[]byte{},
	)

	hash := sha256.Sum256(data)
	return hash[:]
}


func NewGenesisBlock(slotLeader []byte, privateKey *ecdsa.PrivateKey, registryKeys [][]byte, randomness []byte) *Block {
	n := len(registryKeys)
	if n == 0 {
		log.Panic("No registries provided for genesis block")
	}

	stakeData := make(map[string]uint64, n)
	for _, key := range registryKeys {
		stakeData[hex.EncodeToString(key)] = 0
	}

	genesisBlock := Block{
		Index:            0,
		Timestamp:        time.Now().Unix(),
		SlotNumber:       0,
		SlotLeader:       slotLeader,
		Signature:        []byte{},
		IndexHash:        []byte{},
		MerkleRootHash:   []byte{},
		StakeData:        stakeData,
		Transactions:     []Transaction{},
		PrevHash:         randomness, // Storing randomness in PrevHash field
		Hash:             []byte{},
		StakeMapHash:     NewStakeMap().Hash(),
		UnstakeQueueHash: NewUnstakeQueue().Hash(),
	}

	genesisBlock.Signature = genesisBlock.SignBlock(privateKey)
	genesisBlock.Hash = genesisBlock.ComputeHash()

	return &genesisBlock
}

func (b *Block) ComputeHash() []byte {
	data := bytes.Join(
		[][]byte{
			IntToByteArr(b.Index),
			IntToByteArr(b.Timestamp),
			IntToByteArr(b.SlotNumber),
			b.SlotLeader,
			b.Signature,
			b.IndexHash,
			b.BalanceLedgerHash,
			b.CommitStoreHash,
			b.MerkleRootHash,
			b.StakeMapHash,
			b.UnstakeQueueHash,
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

func ValidateBlock(newBlock *Block, oldBlock *Block, slotLeaderKey []byte,
	ledger *BalanceLedger, im DomainIndexer, expiryChecker ExpiryChecker,
	slotsPerDay int64, stakeMap StakeStorer, unstakeQueue *UnstakeQueue,
	slashedEvidence map[string]bool, cs *CommitStore) (*BalanceLedger, StakeStorer, *UnstakeQueue, map[string]bool, *CommitOverlay, bool) {

	if oldBlock.Index+1 != newBlock.Index {
		return nil, nil, nil, nil, nil, false
	}

	if newBlock.SlotNumber <= oldBlock.SlotNumber {
		return nil, nil, nil, nil, nil, false
	}

	if !bytes.Equal(oldBlock.Hash, newBlock.PrevHash) {
		return nil, nil, nil, nil, nil, false
	}

	if !bytes.Equal(newBlock.SlotLeader, slotLeaderKey) {
		return nil, nil, nil, nil, nil, false
	}

	if !newBlock.VerifyBlock(slotLeaderKey) {
		return nil, nil, nil, nil, nil, false
	}

	if !bytes.Equal(newBlock.MerkleRootHash, newBlock.SetupMerkleTree()) {
		return nil, nil, nil, nil, nil, false
	}

	if !bytes.Equal(newBlock.Hash, newBlock.ComputeHash()) {
		return nil, nil, nil, nil, nil, false
	}

	// Staging clones — mutations stay isolated until the block is accepted
	stagingStake := stakeMap.Clone()
	stagingQueue := unstakeQueue.Clone()
	stagingSlashed := make(map[string]bool, len(slashedEvidence))
	for k, v := range slashedEvidence {
		stagingSlashed[k] = v
	}

	// CommitOverlay logic
	var commitOverlay *CommitOverlay
	if cs != nil {
		commitOverlay = NewCommitOverlay(cs.ExportPending(), newBlock.Index)
		commitOverlay.PurgeExpired(newBlock.Index)
	}

	// Mature any queued unstakes before processing this block's transactions
	stagingQueue.SweepMature(uint64(newBlock.SlotNumber))

	// Purge-slot revocation check
	if expiryChecker != nil {
		expectedPurges := expiryChecker.GetPurgeableDomains(newBlock.SlotNumber)

		renewedInBlock := make(map[string]bool)
		for _, tx := range newBlock.Transactions {
			if tx.Type == RENEW {
				renewedInBlock[tx.DomainName] = true
			}
		}

		for _, expected := range expectedPurges {
			if renewedInBlock[expected.DomainName] {
				continue
			}

			found := false
			for _, tx := range newBlock.Transactions {
				if tx.Type == REVOKE &&
					tx.DomainName == expected.DomainName &&
					tx.RedeemsTxID == expected.TID {
					found = true
					break
				}
			}
			if !found {
				log.Println("Block missing required purge:", expected.DomainName, "TID:", expected.TID)
				return nil, nil, nil, nil, nil, false
			}
		}
	}

	// 3-gate re-check: malicious leader may bypass mempool
	if !ValidateTransactions(newBlock.Transactions, ledger, im, commitOverlay,
		newBlock.SlotNumber, slotsPerDay, true, newBlock.SlotLeader, newBlock.Index,
		stakeMap, unstakeQueue, slashedEvidence) {
		log.Printf("ValidateBlock: ValidateTransactions failed — block rejected")
		return nil, nil, nil, nil, nil, false
	}

	// nonce + fee on staging clone
	staging := ledger.Clone()
	for _, tx := range newBlock.Transactions {
		ApplyLedgerMutations(tx, staging)
	}
	totalFees := uint64(0)
	for _, tx := range newBlock.Transactions {
		totalFees += tx.Fee
	}
	if totalFees > 0 {
		staging.Credit(hex.EncodeToString(newBlock.SlotLeader), totalFees)
	}
	if !bytes.Equal(newBlock.BalanceLedgerHash, staging.Hash()) {
		log.Printf("ValidateBlock: BalanceLedgerHash mismatch — block rejected")
		return nil, nil, nil, nil, nil, false
	}

	// domain mutations on overlay
	for i, tx := range newBlock.Transactions {
		ApplyDomainMutations(tx, staging, im, commitOverlay, newBlock.SlotNumber, newBlock.Index, i, slotsPerDay)
	}
	if !bytes.Equal(newBlock.IndexHash, im.GetIndexHash()) {
		log.Printf("ValidateBlock: IndexHash mismatch — block rejected")
		return nil, nil, nil, nil, nil, false
	}
	
	if commitOverlay != nil && !bytes.Equal(newBlock.CommitStoreHash, commitOverlay.Hash()) {
		log.Printf("ValidateBlock: CommitStoreHash mismatch — block rejected")
		return nil, nil, nil, nil, nil, false
	}

	// Verify stake hashes match the staging state after all mutations
	if !bytes.Equal(newBlock.StakeMapHash, stagingStake.Hash()) {
		log.Printf("ValidateBlock: StakeMapHash mismatch — block rejected")
		return nil, nil, nil, nil, nil, false
	}
	if !bytes.Equal(newBlock.UnstakeQueueHash, stagingQueue.Hash()) {
		log.Printf("ValidateBlock: UnstakeQueueHash mismatch — block rejected")
		return nil, nil, nil, nil, nil, false
	}

	// commit domain overlay to real state
	im.Commit()

	return staging, stagingStake, stagingQueue, stagingSlashed, commitOverlay, true
}

// ValidateTransactions implements the 3-gate validation model.
// Gate 1: Signature + spent check + structural guards
// Gate 2: Balance sufficiency (running shadow balance)
// Gate 3: Nonce sequential equality
func ValidateTransactions(txs []Transaction, ledger *BalanceLedger, im DomainIndexer, cs CommitStorer,
	currentSlot int64, slotsPerDay int64,
	isBlockValidation bool, slotLeader []byte, blockIndex int64,
	stakeMap StakeStorer, unstakeQueue *UnstakeQueue, slashedEvidence map[string]bool) bool {

	shadowNonce := make(map[string]uint64)
	shadowBalance := make(map[string]uint64)
	shadowSpent := make(map[int]bool)
	shadowForSale := make(map[string]uint64)
	shadowRegistered := make(map[string]bool)
	shadowOwner := make(map[string]string)
	shadowTx := make(map[int]*Transaction)
	shadowStake := make(map[string]uint64)        // intra-block stake mutations
	shadowPendingStake := make(map[string]uint64) // UNSTAKE coins queued this block
	shadowQueueBurn := make(map[string]uint64)    // queue burns from equivocation this block
	slashedEquivocations := make(map[string]bool) // offense keys seen this block
	shadowCommit := make(map[string]bool)

	for _, tx := range txs {
		// Gate 1: Signature verification
		if len(tx.OwnerKey) > 0 {
			if !VerifySignature(tx.OwnerKey, &tx) {
				log.Printf("ValidateTransactions: invalid signature for domain %s", tx.DomainName)
				return false
			}
		}

		// Gate 1.5p: PoS field hygiene
		switch tx.Type {
		case STAKE, UNSTAKE:
			if tx.DomainName != "" || len(tx.Records) > 0 || tx.RedeemsTxID != 0 || len(tx.Recipient) > 0 {
				log.Printf("ValidateTransactions: %d rejected — legacy fields must be empty", tx.Type)
				return false
			}
			if len(tx.EquivBlockA) > 0 || len(tx.EquivBlockB) > 0 {
				log.Printf("ValidateTransactions: %d rejected — equivocation fields must be empty", tx.Type)
				return false
			}
		case EQUIVOCATION_PROOF:
			if tx.DomainName != "" || len(tx.Records) > 0 || tx.RedeemsTxID != 0 || len(tx.Recipient) > 0 {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — legacy fields must be empty")
				return false
			}
			if tx.StakeAmount != 0 {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — StakeAmount must be 0")
				return false
			}
		}

		// Gate 1.5q: Inverse hygiene — legacy types must not carry PoS fields
		switch tx.Type {
		case REGISTER, UPDATE, REVOKE, RENEW, LIST, BUY, TRANSFER, DELIST, FUND, COMMIT, REVEAL:
			if tx.StakeAmount != 0 {
				log.Printf("ValidateTransactions: %d rejected — StakeAmount must be 0", tx.Type)
				return false
			}
			if len(tx.EquivBlockA) > 0 || len(tx.EquivBlockB) > 0 {
				log.Printf("ValidateTransactions: %d rejected — equivocation fields must be empty", tx.Type)
				return false
			}
		}

		// Gate 1.5r: REGISTER is deprecated — use COMMIT→REVEAL
		if tx.Type == REGISTER {
			log.Printf("REGISTER rejected (use COMMIT→REVEAL), TID=%d", tx.TID)
			return false
		}

		// Gate 1.5s: CommitHash and Salt must be empty for non-COMMIT/REVEAL types
		if tx.Type != COMMIT && tx.Type != REVEAL {
			if len(tx.CommitHash) > 0 {
				log.Printf("CommitHash must be empty for type %d, TID=%d", tx.Type, tx.TID)
				return false
			}
			if len(tx.Salt) > 0 {
				log.Printf("Salt must be empty for type %d, TID=%d", tx.Type, tx.TID)
				return false
			}
		}

		// Gate 1.5: RedeemsTxID double-spend check (REVEAL included for defense-in-depth)
		if tx.RedeemsTxID != 0 && (tx.Type == REGISTER || tx.Type == UPDATE || tx.Type == RENEW || tx.Type == REVOKE || tx.Type == REVEAL) {
			if shadowSpent[tx.RedeemsTxID] {
				log.Printf("ValidateTransactions: RedeemsTxID %d already spent in this block",
					tx.RedeemsTxID)
				return false
			}
			if im.IsSpent(tx.RedeemsTxID) {
				log.Printf("ValidateTransactions: RedeemsTxID %d already spent (prior block)",
					tx.RedeemsTxID)
				return false
			}

			// Gate 1.5h: Domain-affinity check (skip for REVEAL — COMMIT lives in CommitStore)
			if tx.Type != REGISTER && tx.Type != REVEAL {
				redeemsTx, inShadow := shadowTx[tx.RedeemsTxID]
				if !inShadow {
					redeemsTx = im.GetTxByID(tx.RedeemsTxID)
				}
				if redeemsTx == nil {
					log.Printf("ValidateTransactions: RedeemsTxID %d not found", tx.RedeemsTxID)
					return false
				}
				if redeemsTx.DomainName != tx.DomainName {
					log.Printf("ValidateTransactions: RedeemsTxID %d belongs to %s, not %s",
						tx.RedeemsTxID, redeemsTx.DomainName, tx.DomainName)
					return false
				}
			}

			shadowSpent[tx.RedeemsTxID] = true
		}

		// Gate 1.5b: Reject LIST with zero price
		if tx.Type == LIST && tx.ListPrice == 0 {
			log.Printf("ValidateTransactions: LIST %s has ListPrice=0", tx.DomainName)
			return false
		}

		// Gate 1.5c: Reject REGISTER/UPDATE/RENEW with empty records
		if (tx.Type == REGISTER || tx.Type == UPDATE || tx.Type == RENEW) && len(tx.Records) == 0 {
			log.Printf("ValidateTransactions: %d rejected — empty records", tx.Type)
			return false
		}

		// Gate 1.5d: Reject TRANSFER with nil/empty Recipient
		if tx.Type == TRANSFER && len(tx.Recipient) == 0 {
			log.Printf("ValidateTransactions: TRANSFER rejected — Recipient is empty")
			return false
		}

		// Gate 1.5f: REGISTER must have RedeemsTxID == 0
		if tx.Type == REGISTER && tx.RedeemsTxID != 0 {
			log.Printf("ValidateTransactions: REGISTER rejected — RedeemsTxID must be 0")
			return false
		}

		// Gate 1.5g: UPDATE/RENEW/REVOKE must have non-zero RedeemsTxID
		if (tx.Type == UPDATE || tx.Type == RENEW || tx.Type == REVOKE) && tx.RedeemsTxID == 0 {
			log.Printf("ValidateTransactions: %d rejected — RedeemsTxID must be non-zero", tx.Type)
			return false
		}

		// Gate 1.5c-commit: COMMIT structural checks
		if tx.Type == COMMIT {
			if !IsRegistryKey(tx.OwnerKey) {
				log.Printf("ValidateTransactions(Commit): Not TrustedRegistry, TID=%d", tx.TID)
				return false
			}
			if len(tx.CommitHash) != 32 {
				log.Printf("ValidateTransactions(Commit): Invalid CommitHash length %d, TID=%d", len(tx.CommitHash), tx.TID)
				return false
			}
			if tx.DomainName != "" {
				log.Printf("ValidateTransactions(Commit): DomainName must be blank, TID=%d", tx.TID)
				return false
			}
			if len(tx.Records) > 0 {
				log.Printf("ValidateTransactions(Commit): Records must be empty, TID=%d", tx.TID)
				return false
			}
			if tx.RedeemsTxID != 0 {
				log.Printf("ValidateTransactions(Commit): RedeemsTxID must be 0, TID=%d", tx.TID)
				return false
			}
			if len(tx.Salt) > 0 {
				log.Printf("ValidateTransactions(Commit): Salt must be blank, TID=%d", tx.TID)
				return false
			}
			commitHex := hex.EncodeToString(tx.CommitHash)
			if shadowCommit[commitHex] {
				log.Printf("ValidateTransactions(Commit): Duplicate hash in block, TID=%d", tx.TID)
				return false
			}
			if cs != nil && cs.GetCommit(commitHex) != nil {
				log.Printf("ValidateTransactions(Commit): Hash already pending, TID=%d", tx.TID)
				return false
			}
			shadowCommit[commitHex] = true
		}

		// Gate 1.5e: Domain lifecycle phase check for secondary market ops
		if tx.Type == LIST || tx.Type == DELIST || tx.Type == BUY || tx.Type == TRANSFER {
			existingTx := im.GetDomain(tx.DomainName)
			if existingTx == nil {
				if !shadowRegistered[tx.DomainName] {
					log.Printf("ValidateTransactions: %d rejected — domain %s not found",
						tx.Type, tx.DomainName)
					return false
				}
			} else {
				if !shadowRegistered[tx.DomainName] {
					phase := GetDomainPhase(currentSlot, existingTx.ExpirySlot, slotsPerDay)
					if phase != "active" {
						log.Printf("ValidateTransactions: %d rejected — domain %s is in %s phase",
							tx.Type, tx.DomainName, phase)
						return false
					}
				}
			}
		}

		// Auto-revocations: no OwnerKey, special handling
		if len(tx.OwnerKey) == 0 {
			if tx.Type == REVOKE {
				if !isBlockValidation {
					log.Printf("ValidateTransactions: auto-REVOKE rejected in mempool context")
					return false
				}
				if !VerifySignature(slotLeader, &tx) {
					log.Printf("ValidateTransactions: auto-REVOKE not signed by slot leader")
					return false
				}
				// Defense: reject auto-REVOKE for domains registered in this block
				if shadowRegistered[tx.DomainName] {
					log.Printf("ValidateTransactions: auto-REVOKE rejected — %s registered in this block", tx.DomainName)
					return false
				}
				continue
			}
			log.Printf("ValidateTransactions: rejected %d with empty OwnerKey", tx.Type)
			return false
		}

		pubKeyHex := hex.EncodeToString(tx.OwnerKey)

		// Read running balance
		currentBalance := ledger.GetBalance(pubKeyHex)
		if b, ok := shadowBalance[pubKeyHex]; ok {
			currentBalance = b
		}

		// Gate 2: Minimum fee floor
		const MinFee uint64 = 1
		if tx.Fee < MinFee {
			log.Printf("ValidateTransactions: fee too low — %s offered %d, minimum is %d",
				pubKeyHex[:8], tx.Fee, MinFee)
			return false
		}

		// Gate 2a: FUND restricted to TrustedRegistries
		if tx.Type == FUND {
			isTrusted := false
			for _, regKey := range TrustedRegistries {
				if hex.EncodeToString(regKey) == pubKeyHex {
					isTrusted = true
					break
				}
			}
			if !isTrusted {
				log.Printf("ValidateTransactions: FUND rejected — %s is not a TrustedRegistry",
					pubKeyHex[:8])
				return false
			}
			if tx.ListPrice == 0 || len(tx.Recipient) == 0 {
				log.Printf("ValidateTransactions: FUND rejected — must have amount > 0 and recipient")
				return false
			}
			// Overflow-safe arithmetic
			totalNeeded := tx.Fee + tx.ListPrice
			if totalNeeded < tx.Fee || totalNeeded < tx.ListPrice {
				log.Printf("ValidateTransactions: FUND rejected — fee + amount overflows uint64")
				return false
			}
			if currentBalance < totalNeeded {
				log.Printf("ValidateTransactions: FUND rejected — %s has %d, needs %d",
					pubKeyHex[:8], currentBalance, totalNeeded)
				return false
			}
		}

		// Gate 2: Balance sufficiency
		if currentBalance < tx.Fee {
			log.Printf("ValidateTransactions: insufficient balance — %s has %d, needs %d",
				pubKeyHex[:8], currentBalance, tx.Fee)
			return false
		}

		// Read running nonce
		currentNonce := ledger.GetNonce(pubKeyHex)
		if n, ok := shadowNonce[pubKeyHex]; ok {
			currentNonce = n
		}

		// Gate 3: Nonce check
		if tx.Nonce != currentNonce {
			log.Printf("ValidateTransactions: nonce mismatch for %s — expected %d, got %d",
				pubKeyHex[:8], currentNonce, tx.Nonce)
			return false
		}

		// Running balance after fee
		runningBalance := currentBalance - tx.Fee

		// FUND: deduct transfer amount from running balance
		if tx.Type == FUND {
			runningBalance -= tx.ListPrice
		}

		// BUY: verify listing, slippage, self-buy, deduct price
		if tx.Type == BUY {
			sfPrice, inShadow := shadowForSale[tx.DomainName]
			isListed := inShadow && sfPrice > 0
			listPrice := sfPrice
			if !inShadow {
				isListed = im.IsForSale(tx.DomainName)
				listPrice = im.GetListPrice(tx.DomainName)
			}
			if !isListed {
				log.Printf("ValidateTransactions: BUY rejected — %s not listed ForSale",
					tx.DomainName)
				return false
			}
			if runningBalance < listPrice {
				log.Printf("ValidateTransactions: BUY rejected — %s has %d after fee, needs %d",
					pubKeyHex[:8], runningBalance, listPrice)
				return false
			}
			if tx.ListPrice < listPrice {
				log.Printf("ValidateTransactions: BUY rejected — buyer maxPrice %d < listPrice %d",
					tx.ListPrice, listPrice)
				return false
			}
			runningBalance -= listPrice

			// Self-buy guard
			currentOwner := ""
			if so, ok := shadowOwner[tx.DomainName]; ok {
				currentOwner = so
			} else {
				owner := im.GetOwner(tx.DomainName)
				if owner != nil {
					currentOwner = hex.EncodeToString(owner)
				}
			}
			if currentOwner == pubKeyHex {
				log.Printf("ValidateTransactions: BUY rejected — buyer %s is also the owner",
					pubKeyHex[:8])
				return false
			}

			shadowForSale[tx.DomainName] = 0
		}

		// LIST/DELIST: ownership check + shadow state
		if tx.Type == LIST || tx.Type == DELIST {
			currentOwner := ""
			if so, ok := shadowOwner[tx.DomainName]; ok {
				currentOwner = so
			} else {
				owner := im.GetOwner(tx.DomainName)
				if owner != nil {
					currentOwner = hex.EncodeToString(owner)
				}
			}
			if currentOwner != pubKeyHex {
				log.Printf("ValidateTransactions: %d rejected — %s is not the owner of %s",
					tx.Type, pubKeyHex[:8], tx.DomainName)
				return false
			}
			if tx.Type == LIST {
				shadowForSale[tx.DomainName] = tx.ListPrice
			} else {
				isListed := false
				if sfPrice, inShadow := shadowForSale[tx.DomainName]; inShadow {
					isListed = sfPrice > 0
				} else {
					isListed = im.IsForSale(tx.DomainName)
				}
				if !isListed {
					log.Printf("ValidateTransactions: DELIST rejected — %s is not listed for sale",
						tx.DomainName)
					return false
				}
				shadowForSale[tx.DomainName] = 0
			}
		}

		// UPDATE/REVOKE: existence + phase + ownership check
		if tx.Type == UPDATE || tx.Type == REVOKE {
			existingTx := im.GetDomain(tx.DomainName)
			if existingTx == nil {
				if !shadowRegistered[tx.DomainName] {
					log.Printf("ValidateTransactions: %d rejected — domain %s does not exist",
						tx.Type, tx.DomainName)
					return false
				}
			} else {
				if !shadowRegistered[tx.DomainName] {
					phase := GetDomainPhase(currentSlot, existingTx.ExpirySlot, slotsPerDay)
					if phase == "purged" {
						log.Printf("ValidateTransactions: %d rejected — domain %s is in purged phase",
							tx.Type, tx.DomainName)
						return false
					}
				}
			}
			currentOwner := ""
			if so, ok := shadowOwner[tx.DomainName]; ok {
				currentOwner = so
			} else {
				owner := im.GetOwner(tx.DomainName)
				if owner != nil {
					currentOwner = hex.EncodeToString(owner)
				}
			}
			if currentOwner != pubKeyHex {
				log.Printf("ValidateTransactions: %d rejected — %s is not the owner of %s",
					tx.Type, pubKeyHex[:8], tx.DomainName)
				return false
			}
		}

		// RENEW: separate validation — TrustedRegistry signer, no ownership check
		if tx.Type == RENEW {
			existingTx := im.GetDomain(tx.DomainName)
			if existingTx == nil {
				if !shadowRegistered[tx.DomainName] {
					log.Printf("ValidateTransactions: RENEW rejected — domain %s does not exist",
						tx.DomainName)
					return false
				}
			} else {
				if !shadowRegistered[tx.DomainName] {
					phase := GetDomainPhase(currentSlot, existingTx.ExpirySlot, slotsPerDay)
					if phase == "purged" {
						log.Printf("ValidateTransactions: RENEW rejected — domain %s is in purged phase",
							tx.DomainName)
						return false
					}
				}
			}
			isTrusted := false
			for _, regKey := range TrustedRegistries {
				if hex.EncodeToString(regKey) == pubKeyHex {
					isTrusted = true
					break
				}
			}
			if !isTrusted {
				log.Printf("ValidateTransactions: RENEW rejected — %s is not a TrustedRegistry",
					pubKeyHex[:8])
				return false
			}
		}

		// REGISTER: track + validate
		if tx.Type == REGISTER {
			if !IsRegistryKey(tx.OwnerKey) {
				log.Printf("ValidateTransactions: REGISTER rejected — %s is not a TrustedRegistry",
					pubKeyHex[:8])
				return false
			}
			if shadowRegistered[tx.DomainName] {
				log.Printf("ValidateTransactions: REGISTER rejected — %s already registered in this block",
					tx.DomainName)
				return false
			}
			existingTx := im.GetDomain(tx.DomainName)
			if existingTx != nil {
				phase := GetDomainPhase(currentSlot, existingTx.ExpirySlot, slotsPerDay)
				if phase != "purged" {
					log.Printf("ValidateTransactions: REGISTER rejected — %s exists in %s phase",
						tx.DomainName, phase)
					return false
				}
			}
			shadowRegistered[tx.DomainName] = true
			shadowOwner[tx.DomainName] = pubKeyHex
		}

		// REVEAL: 7-gate pipeline
		if tx.Type == REVEAL {
			if !IsRegistryKey(tx.OwnerKey) {
				log.Printf("ValidateTransactions: REVEAL rejected — not TrustedRegistry, TID=%d", tx.TID)
				return false
			}
			if len(tx.CommitHash) > 0 {
				log.Printf("ValidateTransactions: REVEAL rejected — CommitHash must be empty, TID=%d", tx.TID)
				return false
			}
			if len(tx.Recipient) == 0 {
				log.Printf("ValidateTransactions: REVEAL rejected — Recipient required, TID=%d", tx.TID)
				return false
			}
			// Recompute hash using length-prefixed fields
			revealData := make([]byte, 0, 24+len(tx.DomainName)+len(tx.Salt)+len(tx.OwnerKey))
			revealData = append(revealData, IntToByteArr(int64(len(tx.DomainName)))...)
			revealData = append(revealData, []byte(tx.DomainName)...)
			revealData = append(revealData, IntToByteArr(int64(len(tx.Salt)))...)
			revealData = append(revealData, tx.Salt...)
			revealData = append(revealData, IntToByteArr(int64(len(tx.OwnerKey)))...)
			revealData = append(revealData, tx.OwnerKey...)
			recomputedHash := sha256.Sum256(revealData)
			commitHex := hex.EncodeToString(recomputedHash[:])
			// Gate R1: hash match
			var commitRecord *CommitRecord
			if cs != nil {
				commitRecord = cs.GetCommit(commitHex)
			}
			if commitRecord == nil || shadowCommit[commitHex] {
				log.Printf("ValidateTransactions: REVEAL gate R1 — no pending COMMIT for hash %s, TID=%d", commitHex[:16], tx.TID)
				return false
			}
			// Gate R1b: TID binding
			if tx.RedeemsTxID != commitRecord.CommitTID {
				log.Printf("ValidateTransactions: REVEAL gate R1b — TID mismatch, TID=%d", tx.TID)
				return false
			}
			// Gate R2: PubKey binding
			if !bytes.Equal(tx.OwnerKey, commitRecord.CommitterPK) {
				log.Printf("ValidateTransactions: REVEAL gate R2 — PubKey mismatch, TID=%d", tx.TID)
				return false
			}
			// Gate R3: minimum delay
			blocksSinceCommit := blockIndex - commitRecord.CommitBlock
			if blocksSinceCommit < CommitMinDelay {
				log.Printf("ValidateTransactions: REVEAL gate R3 — premature (%d blocks, need %d), TID=%d", blocksSinceCommit, CommitMinDelay, tx.TID)
				return false
			}
			// Gate R4: domain availability
			if shadowRegistered[tx.DomainName] {
				log.Printf("ValidateTransactions: REVEAL gate R4 — domain %s already registered in block, TID=%d", tx.DomainName, tx.TID)
				return false
			}
			existingTx := im.GetDomain(tx.DomainName)
			if existingTx != nil {
				phase := GetDomainPhase(currentSlot, existingTx.ExpirySlot, slotsPerDay)
				if phase != "purged" {
					log.Printf("ValidateTransactions: REVEAL gate R4 — domain %s in %s phase, TID=%d", tx.DomainName, phase, tx.TID)
					return false
				}
				// Gate R4b: COMMIT must post-date purge
				purgeSlot := ComputePurgeSlot(existingTx.ExpirySlot, slotsPerDay)
				if commitRecord.CommitSlot < purgeSlot {
					log.Printf("ValidateTransactions: REVEAL gate R4b — COMMIT predates purge, TID=%d", tx.TID)
					return false
				}
			}
			// All gates passed — mark consumed
			shadowCommit[commitHex] = true
			shadowRegistered[tx.DomainName] = true
			// Shadow owner is the Recipient (end-user), not the Registry
			shadowOwner[tx.DomainName] = hex.EncodeToString(tx.Recipient)
		}

		// TRANSFER: ownership check + shadow update
		if tx.Type == TRANSFER {
			currentOwner := ""
			if so, ok := shadowOwner[tx.DomainName]; ok {
				currentOwner = so
			} else {
				owner := im.GetOwner(tx.DomainName)
				if owner != nil {
					currentOwner = hex.EncodeToString(owner)
				}
			}
			if currentOwner != pubKeyHex {
				log.Printf("ValidateTransactions: TRANSFER rejected — %s is not the owner of %s",
					pubKeyHex[:8], tx.DomainName)
				return false
			}
			shadowOwner[tx.DomainName] = hex.EncodeToString(tx.Recipient)
			shadowForSale[tx.DomainName] = 0
		}
		if tx.Type == BUY {
			shadowOwner[tx.DomainName] = pubKeyHex
		}

		// Gate 3.5: STAKE validation
		if tx.Type == STAKE {
			if tx.StakeAmount == 0 {
				log.Printf("ValidateTransactions: STAKE rejected — StakeAmount must be > 0")
				return false
			}
			currentStake := stakeMap.GetStake(pubKeyHex)
			if s, ok := shadowStake[pubKeyHex]; ok {
				currentStake = s
			}
			if currentStake == 0 && tx.StakeAmount < MinStakeThreshold {
				log.Printf("ValidateTransactions: STAKE rejected — first stake %d below minimum %d",
					tx.StakeAmount, MinStakeThreshold)
				return false
			}
			if tx.Fee+tx.StakeAmount < tx.Fee {
				log.Printf("ValidateTransactions: STAKE rejected — fee + StakeAmount overflows uint64")
				return false
			}
			if runningBalance < tx.StakeAmount {
				log.Printf("ValidateTransactions: STAKE rejected — %s has %d after fee, needs %d",
					pubKeyHex[:8], runningBalance, tx.StakeAmount)
				return false
			}
			runningBalance -= tx.StakeAmount
			shadowStake[pubKeyHex] = currentStake + tx.StakeAmount
		}

		// Gate 3.5: UNSTAKE validation
		if tx.Type == UNSTAKE {
			if tx.StakeAmount == 0 {
				log.Printf("ValidateTransactions: UNSTAKE rejected — StakeAmount must be > 0")
				return false
			}
			currentStake := stakeMap.GetStake(pubKeyHex)
			if s, ok := shadowStake[pubKeyHex]; ok {
				currentStake = s
			}
			if currentStake < tx.StakeAmount {
				log.Printf("ValidateTransactions: UNSTAKE rejected — stake %d < requested %d",
					currentStake, tx.StakeAmount)
				return false
			}
			remaining := currentStake - tx.StakeAmount
			if remaining > 0 && remaining < MinStakeThreshold {
				log.Printf("ValidateTransactions: UNSTAKE rejected — remaining stake %d below minimum %d",
					remaining, MinStakeThreshold)
				return false
			}
			shadowStake[pubKeyHex] = remaining
			shadowPendingStake[pubKeyHex] += tx.StakeAmount
		}

		// Gate 3.5: EQUIVOCATION_PROOF validation
		if tx.Type == EQUIVOCATION_PROOF {
			if len(tx.EquivBlockA) == 0 || len(tx.EquivBlockB) == 0 {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — missing evidence")
				return false
			}
			if len(tx.EquivBlockA) > MaxEvidenceBlockBytes || len(tx.EquivBlockB) > MaxEvidenceBlockBytes {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — evidence exceeds size limit")
				return false
			}
			blockA := DeserializeBlock(tx.EquivBlockA)
			blockB := DeserializeBlock(tx.EquivBlockB)
			if blockA == nil || blockB == nil {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — cannot deserialize evidence")
				return false
			}
			offenderHex := hex.EncodeToString(blockA.SlotLeader)
			if blockA.Index != blockB.Index {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — blocks at different heights")
				return false
			}
			if offenderHex != hex.EncodeToString(blockB.SlotLeader) {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — different slot leaders")
				return false
			}
			if bytes.Equal(blockA.Hash, blockB.Hash) {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — identical block hashes")
				return false
			}
			if !blockA.VerifyBlock(blockA.SlotLeader) || !blockB.VerifyBlock(blockB.SlotLeader) {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — invalid block signature")
				return false
			}
			offenseKey := fmt.Sprintf("%d:%s", blockA.Index, offenderHex)
			if slashedEvidence[offenseKey] || slashedEquivocations[offenseKey] {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — offense already recorded")
				return false
			}
			stakePart := stakeMap.GetStake(offenderHex)
			if s, ok := shadowStake[offenderHex]; ok {
				stakePart = s
			}
			queuePart := unstakeQueue.GetPendingStake(offenderHex) + shadowPendingStake[offenderHex] - shadowQueueBurn[offenderHex]
			totalSlashable := stakePart + queuePart
			if totalSlashable == 0 {
				log.Printf("ValidateTransactions: EQUIVOCATION_PROOF rejected — no slashable stake")
				return false
			}
			slashAmount := ComputeSlashAmount(totalSlashable, SlashingPercent)
			if slashAmount <= stakePart {
				shadowStake[offenderHex] = stakePart - slashAmount
			} else {
				shadowStake[offenderHex] = 0
				excess := slashAmount - stakePart
				shadowQueueBurn[offenderHex] += excess
				if shadowPendingStake[offenderHex] > excess {
					shadowPendingStake[offenderHex] -= excess
				} else {
					shadowPendingStake[offenderHex] = 0
				}
			}
			slashedEquivocations[offenseKey] = true
		}

		// Update running shadow state
		shadowNonce[pubKeyHex] = currentNonce + 1
		shadowBalance[pubKeyHex] = runningBalance

		// Populate shadowTx for state-mutating ops
		if tx.Type == REGISTER || tx.Type == UPDATE || tx.Type == RENEW || tx.Type == REVOKE ||
			tx.Type == COMMIT || tx.Type == REVEAL {
			txCopy := tx
			shadowTx[tx.TID] = &txCopy
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
