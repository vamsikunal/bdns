# B-DNS — Blockchain Domain Name System

A **Proof-of-Stake blockchain** that replaces centralised DNS registries with a cryptographically secure, immutable ledger. Domain ownership is enforced by ECDSA key pairs — no registrar can seize or censor a domain. Registrations use a **commit-reveal protocol** to prevent front-running. Records are resolved via standard UDP DNS (RFC 1035) and can be traded on a built-in marketplace using the native **B-Coin** utility token. Everything runs in-process — no Docker, no external services.

---

## Requirements

- **Go 1.22+**

```bash
go mod tidy
```

---

## Quickstart

```bash
# Default — randomised probabilistic simulation, 60 s, 10 nodes
go run main.go

# Run a specific simulation
go run main.go -sim simple    # Basic smoke test: STAKE → COMMIT → REVEAL → DNS query
go run main.go -sim feature   # Multi-record types, CNAME chain resolution, RENEW
go run main.go -sim ledger    # Marketplace economics: LIST, BUY, TRANSFER, DELIST
go run main.go -sim stake     # Token-weighted PoS: staking, slashing, unbonding delay
go run main.go -sim gateway   # Full-stack: gRPC gateway, UDP DNS, light-node failover

# Clean up BoltDB state files between runs
make clear
```

---

## Simulations

| Flag | Nodes | What it validates |
|------|-------|-------------------|
| `simple` | 6 | Core flow — stake, commit-reveal registration, DNS resolution |
| `feature` | 10 | Multi-record (A / AAAA / MX / TXT / CNAME), CNAME loop detection, RENEW |
| `ledger` | 6 | B-Coin economics — FUND, LIST, BUY, TRANSFER, DELIST |
| `stake` | 6 | Token-weighted leader election, UNSTAKE unbonding, equivocation slashing |
| `gateway` | 4 | gRPC `BroadcastTransaction`, UDP DNS queries, light-node header streaming, pool failover |
| `rand` *(default)* | 10 | Probabilistic load — random transactions, DNS queries, renewals over 60 s |

> The `gateway` simulation is the most comprehensive end-to-end test. Run it first when verifying the full stack.

---

## Transaction Types

| Type | Description |
|------|-------------|
| `COMMIT` | Phase 1 of registration — submits `SHA-256(domain ‖ salt ‖ ownerKey)` without revealing the domain name |
| `REVEAL` | Phase 2 — reveals the domain name, salt, and DNS records; links back to the COMMIT via `RedeemsTxID` |
| `UPDATE` | Replace the DNS record set on an active domain |
| `RENEW` | Extend domain expiry during the grace period (signed by the trusted registry) |
| `REVOKE` | Permanently burn a domain (manual or automatic at purge slot) |
| `LIST` | Put a domain on the marketplace at a declared asking price |
| `BUY` | Atomic purchase — debits buyer, credits seller, transfers ownership in one step |
| `DELIST` | Remove an active marketplace listing |
| `TRANSFER` | Direct ownership transfer without marketplace involvement |
| `FUND` | B-Coin transfer between accounts (trusted registry only, used to bootstrap balances) |
| `STAKE` | Lock liquid B-Coins as validator stake to participate in leader election |
| `UNSTAKE` | Queue staked coins for withdrawal (1000-slot unbonding delay before becoming liquid) |
| `EQUIVOCATION_PROOF` | Slash a validator's stake when they sign two conflicting blocks at the same height |

---

## Key Protocol Rules

**Domain lifecycle:** `Active` → `Grace Period` (30 days in slots) → `Purged` (auto-revoked, name available for re-registration)

**Commit-Reveal:**
- A minimum of **3 slots** must elapse between `COMMIT` and `REVEAL` (`CommitMinDelay`)
- A `COMMIT` expires if not revealed within **100 slots** (`CommitMaxWindow`)
- The commit hash binds the owner's **public key** — knowing the domain name and salt alone is insufficient to steal the registration

**Token-PoS:**
- Leader election probability is proportional to locked stake: $P(R_i) = s_i / \sum s_m$
- A `MinStakeThreshold` of 1,000 B-Coins is required for Sybil protection
- Equivocation (double-signing) results in total slash of `StakeMap` + `UnstakeQueue` balances

**DNS Records:** `A` · `AAAA` · `MX` (with priority) · `TXT` · `CNAME`  
Records are canonically sorted by `(Type, Priority, Value)` before hashing so all nodes produce identical `IndexHash` values regardless of network delivery order.

---

## Project Layout

```
.
├── main.go                        # -sim flag dispatch
├── Makefile                       # make run / make clear
├── internal/
│   ├── blockchain/
│   │   ├── transaction.go         # All 13 tx types — construction, signing, verification
│   │   ├── block.go               # Block structure, Merkle root, serialisation
│   │   ├── blockchain.go          # Chain storage (BoltDB), FindTransaction, IsSpent
│   │   ├── apply.go               # State transitions per transaction type
│   │   ├── balance_ledger.go      # B-Coin account balances and nonce tracking
│   │   ├── stake.go               # StakeMap, leader election weight, slashing
│   │   ├── commit.go              # CommitStore — pending registration state machine
│   │   ├── config.go              # Protocol constants (CommitMinDelay=3, GracePeriod=30d, MinStake=1000)
│   │   ├── key_pair.go            # ECDSA P-256 key generation and helpers
│   │   └── merkle_tree.go         # Merkle proof construction for SPV
│   ├── consensus/                 # Slot timing, DRG randomness beacon, leader selection
│   ├── gateway/                   # gRPC server (full nodes), ConnectionPool + failover (light nodes)
│   ├── network/                   # libp2p node, GossipSub transaction propagation, HeaderChain
│   ├── index/                     # Domain → record index backed by BoltDB, IndexOverlay
│   └── proto/                     # Protobuf definitions for the gRPC wire format
├── sims/                          # Self-contained simulation scripts and scenario docs
└── chaindata/                     # BoltDB runtime files (git-ignored)
```

---

## Development

```bash
# Run the full test suite
go test ./...

# Lint
golangci-lint run
golangci-lint run --fix
```
