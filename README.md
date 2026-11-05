# intents-guard

**A policy gate between an autonomous agent and real money on NEAR Intents.**

An agent that can swap tokens cross-chain can also drain a wallet. `intents-guard` sits between
the agent's intent and the money: it prices the swap against the live **NEAR Intents 1Click API**,
runs a declarative policy, and only then lets a deposit address — the object that can actually
move funds — come into existence. A quote that fails the policy stays a *proposal*.

Built for **NEAR HACKATHON** (Nov 2026). Tracks: **01 Unstoppable DeFi**, **06 AI Security & Defense**, **07 Agent Economy**.

```
$ intents-guard quote -from wNEAR -to USDT -amount 1
route      wNEAR (near) → USDT (near)
in         1.000000 wNEAR  (~$4.02)
out        4.017398 USDT  (~$4.015055856966)
min out    3.977224 USDT
estimate   20s
correlation 6ad8ee9c-6048-405a-8132-8d2f68d104b3
deposit    —  (dry: no deposit address created, nothing can move)

$ intents-guard guard -from wNEAR -to USDT -amount 6 -usd 24.12 -policy policy.json
policy     agent-swap-policy/v1
  ✅ origin asset allowed
  ✅ destination asset allowed
  ✅ slippage 100 bps within 150 bps
  ✅ swap size $24.12 within per-swap cap $50.00
  ✅ daily budget ok ($0.00 of $150.00 used)
  ⛔ human approval required: $24.12 is at or above the $20.00 threshold

BLOCKED — no deposit address was created, no funds can move.
```

Both outputs above are real, unedited runs from 2026-11-05 against `https://1click.chaindefuser.com`.

---

## Why this exists

Every "AI agent with a wallet" demo shows the happy path: the agent swaps, the money moves.
The interesting engineering is the other path. Three failure modes that a prompt cannot fix:

1. **The agent asks for a size it should not have.** Spend caps must live outside the model,
   in code that runs before a deposit address exists.
2. **The agent is talked into a bad route.** Asset allowlists and slippage ceilings are policy,
   not suggestions in a system prompt.
3. **Nobody can prove what was approved.** Every decision must be reproducible and logged with
   the API's `correlationId` and quote signature.

`intents-guard` is deliberately **small and auditable**: ~600 lines of Go, no framework, one
HTTP dependency (`net/http`). A security reviewer can read all of it in ten minutes.

## How the gate works

```
agent intent ──► 1Click /v0/tokens ──► resolve assets
                      │
                      ▼
              1Click /v0/quote (dry=true)   ← prices the route, creates NOTHING
                      │
                      ▼
              policy engine (pure function, unit-tested)
                 ├─ asset allowlists
                 ├─ per-swap USD cap
                 ├─ daily USD ledger (UTC day, dry runs excluded)
                 ├─ slippage ceiling
                 ├─ approval threshold  → needs_approval
                 └─ kill switch
                      │
        ┌─────────────┴─────────────┐
     BLOCKED                    ALLOWED (+ -approve if required)
   exit code 1                        │
   no address created                 ▼
                         1Click /v0/quote (dry=false)
                            → deposit address exists
                            → ledger entry appended
                            → funding is still the owner's action
```

The tool **never signs a transaction and never holds a key**. It creates the precondition for a
swap, not the swap. That line is intentional: an agent may propose, a human (or a key held by a
separate process) may fund.

## Install and run

Go 1.24+, no CGO, no third-party modules.

```bash
git clone https://github.com/kebostrade/intents-guard
cd intents-guard
go build ./...
go test ./...            # 8 policy tests, no network needed

go run ./cmd/intents-guard tokens -blockchain near   # live asset list from 1Click
go run ./cmd/intents-guard quote  -from wNEAR -to USDT -amount 1
go run ./cmd/intents-guard guard  -from wNEAR -to USDT -amount 6 -usd 24.12 -policy policy.json
```

Optional environment:

| Variable | Meaning |
|---|---|
| `ONECLICK_API_KEY` | partner JWT; unauthenticated quotes pay an extra 25 bps |
| `ONECLICK_BASE_URL` | defaults to `https://1click.chaindefuser.com` |

`go run ... -json` emits machine-readable output for an orchestrating agent.

## Policy file

```json
{
  "name": "agent-swap-policy/v1",
  "allowed_origin_assets": ["nep141:wrap.near", "nep141:usdt.tether-token.near"],
  "allowed_destination_assets": ["nep141:wrap.near", "nep141:usdt.tether-token.near"],
  "max_usd_per_swap": 50,
  "max_usd_daily": 150,
  "max_slippage_bps": 150,
  "require_approval_above_usd": 20,
  "kill_switch": false
}
```

Rules are evaluated in a fixed order and **every** violation is reported, not just the first —
an agent gets a complete picture of why it was stopped.

## What is verified, and what is not

Verified on 2026-11-05 (real network calls, outputs above):

- `tokens` returns the live 1Click asset list (BTC ≈ $80,368, ETH ≈ $2,414 at the time of writing).
- `quote` returns a signed dry quote with `minAmountOut` and a 20 s estimate.
- The policy engine blocks a $24.12 swap that is above the $20 approval threshold, and no
  deposit address is created.
- `go test ./...` — 8 tests on the policy engine (caps, threshold, allowlist, kill switch,
  UTC-day ledger accounting).

**Not verified — stated plainly, because the alternative is a demo that lies:**

- **No live swap has been executed by this repo.** That requires a funded account and a human
  funding the deposit address; the tool intentionally cannot do it alone.
- **`/v0/status` polling is implemented but untested against a real deposit** — there is no
  funded deposit to poll. The code path exists; it is not evidence.
- **Daily ledger** is a local JSONL file, not a distributed store. Two processes writing the
  same ledger concurrently can overshoot the daily cap. For a single agent (this use case) it
  is sufficient; for a fleet it needs a transactional store, and that is stated as future work
  rather than pretended away.
- Named NEAR accounts are not resolved: the tool uses an implicit account id for
  `recipient`/`refundTo`. A human-facing UI would resolve `alice.near` first.

## Judge-facing summary

- **Does it run?** `go build ./...` on a clean checkout, three commands, no keys required for
  the dry path. `go test ./...` passes offline.
- **Does it call NEAR at runtime?** Every `tokens`/`quote`/`guard` invocation is an HTTP call to
  `1click.chaindefuser.com`; there is no cached fixture in the code path.
- **Is the security claim real?** The gate is a pure function with tests, and the deposit address
  is only requested *after* it returns allowed. The blocked case is demonstrated above with the
  real API.

## License

MIT — see `LICENSE`.
