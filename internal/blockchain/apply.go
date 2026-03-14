package blockchain

import (
	"encoding/hex"
	"fmt"
	"log"
)

// ApplyLedgerMutations processes nonce + fee only.
func ApplyLedgerMutations(tx Transaction, ledger *BalanceLedger) {
	if len(tx.OwnerKey) == 0 {
		return
	}
	pubKeyHex := hex.EncodeToString(tx.OwnerKey)

	ledger.IncrementNonce(pubKeyHex)

	if tx.Fee > 0 {
		if err := ledger.Debit(pubKeyHex, tx.Fee); err != nil {
			log.Printf("ApplyLedgerMutations: fee debit failed for %s: %v", pubKeyHex[:8], err)
		}
	}

	if tx.Type == FUND && len(tx.Recipient) > 0 && tx.ListPrice > 0 {
		recipientHex := hex.EncodeToString(tx.Recipient)
		if err := ledger.Debit(pubKeyHex, tx.ListPrice); err == nil {
			ledger.Credit(recipientHex, tx.ListPrice)
		}
	}

	// Move coins from liquid balance into staked balance
	if tx.Type == STAKE && tx.StakeAmount > 0 {
		if err := ledger.Debit(pubKeyHex, tx.StakeAmount); err != nil {
			log.Printf("ApplyLedgerMutations: STAKE debit failed for %s: %v", pubKeyHex[:8], err)
		}
	}
}

// ApplyStakeMutations processes StakeMap / UnstakeQueue / slashedEvidence effects for tx.
func ApplyStakeMutations(tx Transaction, stakeMap StakeStorer,
	unstakeQueue *UnstakeQueue, slashedEvidence map[string]bool, currentSlot int64) {
	// slashedEvidence must be a staging copy — the caller commits it to real state after block finalization.
	if len(tx.OwnerKey) == 0 {
		return
	}
	pubKeyHex := hex.EncodeToString(tx.OwnerKey)

	switch tx.Type {
	case STAKE:
		stakeMap.AddStake(pubKeyHex, tx.StakeAmount)

	case UNSTAKE:
		stakeMap.ReduceStake(pubKeyHex, tx.StakeAmount)
		matureSlot := uint64(currentSlot) + UnstakeDelaySlots
		unstakeQueue.Enqueue(pubKeyHex, tx.StakeAmount, matureSlot)

	case EQUIVOCATION_PROOF:
		if len(tx.EquivBlockA) == 0 {
			return
		}
		blockA := DeserializeBlock(tx.EquivBlockA)
		if blockA == nil {
			return
		}
		offenderHex := hex.EncodeToString(blockA.SlotLeader)
		offenseKey := fmt.Sprintf("%d:%s", blockA.Index, offenderHex)
		if slashedEvidence[offenseKey] {
			return
		}
		totalSlashable := stakeMap.GetStake(offenderHex) + unstakeQueue.GetPendingStake(offenderHex)
		slashAmount := ComputeSlashAmount(totalSlashable, SlashingPercent)
		stakedNow := stakeMap.GetStake(offenderHex)
		if slashAmount <= stakedNow {
			stakeMap.ReduceStake(offenderHex, slashAmount)
		} else {
			stakeMap.ReduceStake(offenderHex, stakedNow)
			unstakeQueue.BurnPending(offenderHex, slashAmount-stakedNow)
		}
		slashedEvidence[offenseKey] = true
	}
}

func ApplyDomainMutations(tx Transaction, ledger *BalanceLedger, im DomainIndexer,
	currentSlot int64, txIndex int, slotsPerDay int64) error {

	pubKeyHex := hex.EncodeToString(tx.OwnerKey)

	switch tx.Type {
	case REGISTER:
		existingTx := im.GetDomain(tx.DomainName)
		if existingTx != nil {
			phase := GetDomainPhase(currentSlot, existingTx.ExpirySlot, slotsPerDay)
			if phase != "purged" {
				return fmt.Errorf("REGISTER failed: domain %s exists in %s phase", tx.DomainName, phase)
			}
		}
		if tx.RedeemsTxID != 0 {
			return fmt.Errorf("REGISTER failed: RedeemsTxID must be 0 for new registrations")
		}
		im.Add(tx.DomainName, &tx, currentSlot, txIndex, slotsPerDay)
		im.SetOwner(tx.DomainName, tx.OwnerKey)

	case REVOKE:
		if len(tx.OwnerKey) > 0 {
			currentOwner := im.GetOwner(tx.DomainName)
			if currentOwner == nil || hex.EncodeToString(currentOwner) != pubKeyHex {
				return fmt.Errorf("REVOKE failed: %s is not the owner of %s",
					pubKeyHex[:8], tx.DomainName)
			}
		}
		if tx.RedeemsTxID != 0 {
			redeemsTx := im.GetTxByID(tx.RedeemsTxID)
			if redeemsTx == nil || redeemsTx.DomainName != tx.DomainName {
				return fmt.Errorf("REVOKE failed: RedeemsTxID %d does not belong to domain %s",
					tx.RedeemsTxID, tx.DomainName)
			}
		}
		// Auto-revocation temporal verification: reject if domain is still active
		if len(tx.OwnerKey) == 0 {
			existingTx := im.GetDomain(tx.DomainName)
			if existingTx == nil {
				return fmt.Errorf("auto-REVOKE failed: domain %s not found", tx.DomainName)
			}
			phase := GetDomainPhase(currentSlot, existingTx.ExpirySlot, slotsPerDay)
			if phase == "active" {
				return fmt.Errorf("auto-REVOKE failed: domain %s is still in active phase", tx.DomainName)
			}
		}
		existingOwner := im.GetOwner(tx.DomainName)
		if tx.RedeemsTxID != 0 {
			im.MarkAsSpent(tx.RedeemsTxID)
		}
		im.Add(tx.DomainName, &tx, currentSlot, txIndex, slotsPerDay)
		if existingOwner != nil {
			im.SetOwner(tx.DomainName, existingOwner)
		}

	case UPDATE:
		currentOwner := im.GetOwner(tx.DomainName)
		if currentOwner == nil || hex.EncodeToString(currentOwner) != pubKeyHex {
			return fmt.Errorf("UPDATE failed: %s is not the owner of %s",
				pubKeyHex[:8], tx.DomainName)
		}
		existingTx := im.GetDomain(tx.DomainName)
		if existingTx != nil {
			phase := GetDomainPhase(currentSlot, existingTx.ExpirySlot, slotsPerDay)
			if phase != "active" {
				return fmt.Errorf("UPDATE failed: domain %s is in %s phase (must be active)",
					tx.DomainName, phase)
			}
		}
		return applyUpdateOrRenew(tx, im, currentSlot, txIndex, slotsPerDay, false)

	case RENEW:
		existingTxRenew := im.GetDomain(tx.DomainName)
		if existingTxRenew != nil {
			phase := GetDomainPhase(currentSlot, existingTxRenew.ExpirySlot, slotsPerDay)
			if phase == "purged" {
				return fmt.Errorf("RENEW failed: domain %s is in purged phase (must re-register)",
					tx.DomainName)
			}
		}
		return applyUpdateOrRenew(tx, im, currentSlot, txIndex, slotsPerDay, true)

	case LIST:
		domTx := im.GetDomain(tx.DomainName)
		if GetDomainPhase(currentSlot, domTx.ExpirySlot, slotsPerDay) != "active" {
			return fmt.Errorf("LIST failed: %s is not in active phase", tx.DomainName)
		}
		currentOwner := im.GetOwner(tx.DomainName)
		if currentOwner == nil {
			return fmt.Errorf("LIST: domain %s not found", tx.DomainName)
		}
		if hex.EncodeToString(currentOwner) != pubKeyHex {
			return fmt.Errorf("LIST: %s is not the owner", pubKeyHex[:8])
		}
		im.SetListPrice(tx.DomainName, tx.ListPrice)

	case BUY:
		return applyBuyDomain(tx, ledger, im, pubKeyHex, currentSlot, txIndex, slotsPerDay)

	case TRANSFER:
		return applyTransferDomain(tx, im, pubKeyHex, currentSlot, txIndex, slotsPerDay)

	case DELIST:
		domTx := im.GetDomain(tx.DomainName)
		if GetDomainPhase(currentSlot, domTx.ExpirySlot, slotsPerDay) != "active" {
			return fmt.Errorf("DELIST failed: %s is not in active phase", tx.DomainName)
		}
		currentOwner := im.GetOwner(tx.DomainName)
		if currentOwner == nil || !im.IsForSale(tx.DomainName) {
			return fmt.Errorf("DELIST: %s is not listed for sale", tx.DomainName)
		}
		if hex.EncodeToString(currentOwner) != pubKeyHex {
			return fmt.Errorf("DELIST: %s is not the owner", pubKeyHex[:8])
		}
		im.SetListPrice(tx.DomainName, 0)

	case FUND:
		// Ledger-only — handled in Phase A
	}
	return nil
}

// applyUpdateOrRenew is the shared mutation logic for UPDATE and RENEW.
func applyUpdateOrRenew(tx Transaction, im DomainIndexer,
	currentSlot int64, txIndex int, slotsPerDay int64, isRenew bool) error {

	opName := "UPDATE"
	if isRenew {
		opName = "RENEW"
	}

	if tx.RedeemsTxID == 0 {
		return fmt.Errorf("%s failed: RedeemsTxID must be non-zero (provenance chain)", opName)
	}
	redeemsTx := im.GetTxByID(tx.RedeemsTxID)
	if redeemsTx == nil || redeemsTx.DomainName != tx.DomainName {
		return fmt.Errorf("%s failed: RedeemsTxID %d does not belong to domain %s",
			opName, tx.RedeemsTxID, tx.DomainName)
	}
	im.MarkAsSpent(tx.RedeemsTxID)

	existingListPrice := im.GetListPrice(tx.DomainName)
	existingOwner := im.GetOwner(tx.DomainName)

	storeTx := tx
	if isRenew {
		currentDomainTx := im.GetDomain(tx.DomainName)
		if currentDomainTx != nil && len(currentDomainTx.Records) > 0 {
			storeTx.Records = currentDomainTx.Records
		}
	}

	im.Add(tx.DomainName, &storeTx, currentSlot, txIndex, slotsPerDay)

	if existingListPrice > 0 {
		im.SetListPrice(tx.DomainName, existingListPrice)
	}
	if existingOwner != nil {
		im.SetOwner(tx.DomainName, existingOwner)
	}
	return nil
}

// applyBuyDomain handles BUY: price transfer + domain ownership change.
func applyBuyDomain(tx Transaction, ledger *BalanceLedger, im DomainIndexer,
	buyerKeyHex string, currentSlot int64, txIndex int, slotsPerDay int64) error {

	domTx := im.GetDomain(tx.DomainName)
	if GetDomainPhase(currentSlot, domTx.ExpirySlot, slotsPerDay) != "active" {
		return fmt.Errorf("BUY failed: %s is not in active phase", tx.DomainName)
	}

	if !im.IsForSale(tx.DomainName) {
		return fmt.Errorf("BUY failed: %s is not listed ForSale", tx.DomainName)
	}

	sellerKeyHex := hex.EncodeToString(im.GetOwner(tx.DomainName))

	if buyerKeyHex == sellerKeyHex {
		return fmt.Errorf("BUY failed: buyer and seller are the same key (%s) — use DELIST",
			buyerKeyHex[:8])
	}

	listPrice := im.GetListPrice(tx.DomainName)
	if tx.ListPrice < listPrice {
		return fmt.Errorf("BUY failed: buyer's MaxPrice %d < current listPrice %d",
			tx.ListPrice, listPrice)
	}

	if err := ledger.Debit(buyerKeyHex, listPrice); err != nil {
		return fmt.Errorf("BUY failed: buyer %s cannot cover listPrice %d: %v",
			buyerKeyHex[:8], listPrice, err)
	}
	ledger.Credit(sellerKeyHex, listPrice)

	im.SetOwner(tx.DomainName, tx.OwnerKey)
	im.SetListPrice(tx.DomainName, 0)
	return nil
}

// applyTransferDomain reassigns domain ownership to tx.Recipient.
func applyTransferDomain(tx Transaction, im DomainIndexer, ownerKeyHex string,
	currentSlot int64, txIndex int, slotsPerDay int64) error {

	domTx := im.GetDomain(tx.DomainName)
	if GetDomainPhase(currentSlot, domTx.ExpirySlot, slotsPerDay) != "active" {
		return fmt.Errorf("TRANSFER failed: %s is not in active phase", tx.DomainName)
	}

	currentOwner := im.GetOwner(tx.DomainName)
	if currentOwner == nil {
		return fmt.Errorf("TRANSFER failed: domain %s not found", tx.DomainName)
	}
	if hex.EncodeToString(currentOwner) != ownerKeyHex {
		return fmt.Errorf("TRANSFER failed: %s is not the owner of %s", ownerKeyHex[:8], tx.DomainName)
	}

	im.SetOwner(tx.DomainName, tx.Recipient)
	im.SetListPrice(tx.DomainName, 0)
	return nil
}
