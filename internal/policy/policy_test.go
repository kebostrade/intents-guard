package policy

import (
	"testing"
	"time"
)

func testPolicy() *Policy {
	return &Policy{
		Name:            "test/v1",
		AllowedOrigin:   []string{"nep141:wrap.near"},
		AllowedDest:     []string{"nep141:usdt.tether-token.near"},
		MaxUSDPerSwap:   50,
		MaxUSDDaily:     150,
		MaxSlippageBps:  150,
		RequireApproval: 20,
	}
}

func view(usd float64, slip int) QuoteView {
	return QuoteView{
		OriginAsset:      "nep141:wrap.near",
		DestinationAsset: "nep141:usdt.tether-token.near",
		AmountInUSD:      usd,
		SlippageBps:      slip,
	}
}

func TestSmallSwapIsAllowed(t *testing.T) {
	d := testPolicy().Evaluate(view(10, 100), 0)
	if !d.Allowed {
		t.Fatalf("$10 swap should be allowed, got %v", d.Reasons)
	}
	if d.NeedsApproval {
		t.Fatal("$10 is below the $20 threshold and must not require approval")
	}
}

func TestThresholdRequiresApprovalButStaysAllowed(t *testing.T) {
	d := testPolicy().Evaluate(view(20, 100), 0)
	if !d.Allowed {
		t.Fatalf("a $20 swap is allowed, only approval is required: %v", d.Reasons)
	}
	if !d.NeedsApproval {
		t.Fatal("$20 is at the threshold, approval must be required")
	}
}

func TestPerSwapCapBlocks(t *testing.T) {
	d := testPolicy().Evaluate(view(51, 100), 0)
	if d.Allowed {
		t.Fatal("$51 exceeds the $50 per-swap cap and must be blocked")
	}
}

func TestDailyBudgetBlocks(t *testing.T) {
	d := testPolicy().Evaluate(view(30, 100), 130)
	if d.Allowed {
		t.Fatal("30 spent today + 130 already used = 160 > 150, must be blocked")
	}
}

func TestSlippageCapBlocks(t *testing.T) {
	if d := testPolicy().Evaluate(view(10, 151), 0); d.Allowed {
		t.Fatal("151 bps exceeds the 150 bps cap and must be blocked")
	}
	if d := testPolicy().Evaluate(view(10, 150), 0); !d.Allowed {
		t.Fatal("exactly 150 bps is within the cap")
	}
}

func TestAssetAllowlistBlocks(t *testing.T) {
	v := view(10, 100)
	v.OriginAsset = "nep141:random.meme-cooking.near"
	if d := testPolicy().Evaluate(v, 0); d.Allowed {
		t.Fatal("an asset outside the allowlist must be blocked")
	}
}

func TestKillSwitchBlocksEverything(t *testing.T) {
	p := testPolicy()
	p.KillSwitch = true
	d := p.Evaluate(view(1, 10), 0)
	if d.Allowed {
		t.Fatal("kill switch must block even a $1 swap")
	}
}

func TestSpentTodayIgnoresOtherDaysAndDryRuns(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	entries := []LedgerEntry{
		{At: now.Add(-1 * time.Hour), AmountUSD: 10},
		{At: now.Add(-25 * time.Hour), AmountUSD: 99}, // yesterday
		{At: now.Add(-2 * time.Hour), AmountUSD: 500, Dry: true},
	}
	if got := SpentToday(entries, now); got != 10 {
		t.Fatalf("SpentToday = %v, want 10", got)
	}
}
