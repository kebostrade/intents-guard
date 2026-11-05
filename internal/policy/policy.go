// Package policy is the guard between an autonomous agent and the money.
//
// The premise: an agent may ask for a swap, but it may not decide its own limits.
// Every quote is evaluated against a declarative policy before any deposit address
// is allowed to exist. A quote that fails becomes a *proposal* the human can approve,
// never an executed trade.
package policy

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

type Policy struct {
	Name            string   `json:"name"`
	AllowedOrigin   []string `json:"allowed_origin_assets"`
	AllowedDest     []string `json:"allowed_destination_assets"`
	MaxUSDPerSwap   float64  `json:"max_usd_per_swap"`
	MaxUSDDaily     float64  `json:"max_usd_daily"`
	MaxSlippageBps  int      `json:"max_slippage_bps"`
	RequireApproval float64  `json:"require_approval_above_usd"`
	KillSwitch      bool     `json:"kill_switch"`
}

// QuoteView is the subset of a quote the policy needs (no dependency on the API client).
type QuoteView struct {
	OriginAsset      string
	DestinationAsset string
	AmountInUSD      float64
	SlippageBps      int
}

type Decision struct {
	Allowed       bool     `json:"allowed"`
	NeedsApproval bool     `json:"needs_approval"`
	Reasons       []string `json:"reasons"`
	ReasonsOK     []string `json:"checks_passed"`
}

func Load(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	if p.MaxUSDPerSwap <= 0 {
		return nil, fmt.Errorf("policy %s: max_usd_per_swap must be > 0", path)
	}
	return &p, nil
}

// Evaluate runs every rule and returns the first blocking reason(s).
// spentTodayUSD is the sum of executed swaps in the current UTC day.
func (p *Policy) Evaluate(q QuoteView, spentTodayUSD float64) Decision {
	d := Decision{Allowed: true}

	if p.KillSwitch {
		return Decision{Allowed: false, Reasons: []string{"kill_switch is on: every swap is blocked"}}
	}

	if len(p.AllowedOrigin) > 0 && !contains(p.AllowedOrigin, q.OriginAsset) {
		d.block(fmt.Sprintf("origin asset %q is not in allowed_origin_assets", q.OriginAsset))
	} else {
		d.ok("origin asset allowed")
	}

	if len(p.AllowedDest) > 0 && !contains(p.AllowedDest, q.DestinationAsset) {
		d.block(fmt.Sprintf("destination asset %q is not in allowed_destination_assets", q.DestinationAsset))
	} else {
		d.ok("destination asset allowed")
	}

	if q.SlippageBps > p.MaxSlippageBps {
		d.block(fmt.Sprintf("slippage %d bps exceeds max %d bps", q.SlippageBps, p.MaxSlippageBps))
	} else {
		d.ok(fmt.Sprintf("slippage %d bps within %d bps", q.SlippageBps, p.MaxSlippageBps))
	}

	if q.AmountInUSD > p.MaxUSDPerSwap {
		d.block(fmt.Sprintf("swap size $%.2f exceeds per-swap cap $%.2f", q.AmountInUSD, p.MaxUSDPerSwap))
	} else {
		d.ok(fmt.Sprintf("swap size $%.2f within per-swap cap $%.2f", q.AmountInUSD, p.MaxUSDPerSwap))
	}

	if p.MaxUSDDaily > 0 {
		if spentTodayUSD+q.AmountInUSD > p.MaxUSDDaily {
			d.block(fmt.Sprintf("daily budget: $%.2f spent + $%.2f exceeds $%.2f",
				spentTodayUSD, q.AmountInUSD, p.MaxUSDDaily))
		} else {
			d.ok(fmt.Sprintf("daily budget ok ($%.2f of $%.2f used)", spentTodayUSD, p.MaxUSDDaily))
		}
	}

	if p.RequireApproval > 0 && q.AmountInUSD >= p.RequireApproval {
		d.NeedsApproval = true
		d.Reasons = append(d.Reasons, fmt.Sprintf(
			"human approval required: $%.2f is at or above the $%.2f threshold", q.AmountInUSD, p.RequireApproval))
	}

	return d
}

func (d *Decision) block(reason string) {
	d.Allowed = false
	d.Reasons = append(d.Reasons, reason)
}

func (d *Decision) ok(check string) { d.ReasonsOK = append(d.ReasonsOK, check) }

func contains(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// ---- daily spend ledger -------------------------------------------------

type LedgerEntry struct {
	At        time.Time `json:"at"`
	Origin    string    `json:"origin_asset"`
	Dest      string    `json:"destination_asset"`
	AmountUSD float64   `json:"amount_usd"`
	Dry       bool      `json:"dry"`
	Note      string    `json:"note,omitempty"`
}

// SpentToday sums non-dry entries for the current UTC day.
func SpentToday(entries []LedgerEntry, now time.Time) float64 {
	var sum float64
	y, m, d := now.UTC().Date()
	for _, e := range entries {
		ey, em, ed := e.At.UTC().Date()
		if e.Dry || ey != y || em != m || ed != d {
			continue
		}
		sum += e.AmountUSD
	}
	return math.Round(sum*100) / 100
}
