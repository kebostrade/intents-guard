// intents-guard — a policy gate between an autonomous agent and NEAR Intents money.
//
//	$ intents-guard tokens                       # what the solver network can route
//	$ intents-guard quote  --from wNEAR --to USDT --amount 1 --usd 4.02
//	$ intents-guard guard  --from wNEAR --to USDT --amount 1 --usd 4.02 --policy policy.json
//
// `guard` is the point of the tool: it prices the swap, runs the policy engine, and
// only then — with --approve and only if the policy says a human is allowed to approve —
// requests a real (non-dry) quote, which is what creates a deposit address.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dimahlu/intents-guard/internal/oneclick"
	"github.com/dimahlu/intents-guard/internal/policy"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "tokens":
		cmdTokens(args)
	case "quote":
		cmdQuote(args)
	case "guard":
		cmdGuard(args)
	case "version", "--version", "-v":
		fmt.Printf("intents-guard %s\n", version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`intents-guard ` + version + ` — policy gate for NEAR Intents swaps

  tokens   [-rpc-url URL] [-json]
  quote    -from SYMBOL -to SYMBOL -amount N [-slippage 100] [-json]
  guard    -from SYMBOL -to SYMBOL -amount N -usd PRICE -policy FILE [-approve] [-json]

Every quote is dry=true by default: it prices the route without creating a deposit
address. A deposit address (the thing that can move money) is only requested when
the policy engine allows the swap AND the caller passes -approve.

Environment:
  ONECLICK_API_KEY   optional partner JWT (unauthenticated quotes pay +25 bps)
  ONECLICK_BASE_URL  default https://1click.chaindefuser.com
`)
}

func baseURL() string {
	if v := os.Getenv("ONECLICK_BASE_URL"); v != "" {
		return v
	}
	return oneclick.DefaultBaseURL
}

func client() *oneclick.Client {
	return oneclick.New(baseURL(), os.Getenv("ONECLICK_API_KEY"))
}

func cmdTokens(args []string) {
	fs := flag.NewFlagSet("tokens", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print raw token list as JSON")
	filter := fs.String("blockchain", "", "only tokens on this blockchain (e.g. near, eth, sol)")
	fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	toks, err := client().Tokens(ctx)
	if err != nil {
		fail(err)
	}
	if *filter != "" {
		var kept []oneclick.Token
		for _, t := range toks {
			if strings.EqualFold(t.Blockchain, *filter) {
				kept = append(kept, t)
			}
		}
		toks = kept
	}
	if *asJSON {
		printJSON(toks)
		return
	}
	sort.Slice(toks, func(i, j int) bool { return toks[i].Symbol < toks[j].Symbol })
	fmt.Printf("%-10s %-10s %-46s %10s\n", "SYMBOL", "CHAIN", "ASSET ID", "PRICE")
	for _, t := range toks {
		fmt.Printf("%-10s %-10s %-46s %10.4f\n", t.Symbol, t.Blockchain, t.AssetID, t.Price)
	}
	fmt.Printf("\n%d routable assets\n", len(toks))
}

func findToken(toks []oneclick.Token, symbol string) (oneclick.Token, error) {
	var nearMatch *oneclick.Token
	for i, t := range toks {
		if !strings.EqualFold(t.Symbol, symbol) {
			continue
		}
		if t.Blockchain == "near" {
			return t, nil
		}
		if nearMatch == nil {
			nearMatch = &toks[i]
		}
	}
	if nearMatch != nil {
		return *nearMatch, nil
	}
	return oneclick.Token{}, fmt.Errorf("symbol %q not offered by 1Click", symbol)
}

// implicitAccount derives a valid NEAR account id from an ed25519 public key.
// It is used as the refund/recipient placeholder when the caller has no named account.
const implicitAccount = "b4c2fbec9d610f9a3a9b843c47b1a8095cec887c7c8d1e1d4d1a4b5e1e6f0a1b"

type priced struct {
	resp   *oneclick.QuoteResponse
	origin oneclick.Token
	dest   oneclick.Token
	usdIn  float64
}

func buildQuote(ctx context.Context, c *oneclick.Client, from, to string, amount float64, usdIn float64, slippage int, dry bool) (*priced, error) {
	toks, err := c.Tokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("token list: %w", err)
	}
	o, err := findToken(toks, from)
	if err != nil {
		return nil, err
	}
	d, err := findToken(toks, to)
	if err != nil {
		return nil, err
	}
	// amount is given in whole tokens; the API wants the smallest unit.
	amt := fmt.Sprintf("%.0f", amount*pow10(o.Decimals))
	if usdIn <= 0 {
		usdIn = amount * o.Price
	}
	req := oneclick.QuoteRequest{
		Dry:               dry,
		SwapType:          "EXACT_INPUT",
		SlippageTolerance: slippage,
		OriginAsset:       o.AssetID,
		DepositType:       "ORIGIN_CHAIN",
		DestinationAsset:  d.AssetID,
		Amount:            amt,
		Recipient:         implicitAccount,
		RecipientType:     "INTENTS",
		RefundTo:          implicitAccount,
		RefundType:        "INTENTS",
		Deadline:          time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339),
	}
	resp, err := c.Quote(ctx, req)
	if err != nil {
		return nil, err
	}
	return &priced{resp: resp, origin: o, dest: d, usdIn: usdIn}, nil
}

func powder(p *priced) {
	q := p.resp.Quote
	out := parseFloat(q.AmountOut) / pow10(p.dest.Decimals)
	fmt.Printf("route      %s (%s) → %s (%s)\n", p.origin.Symbol, p.origin.Blockchain, p.dest.Symbol, p.dest.Blockchain)
	fmt.Printf("in         %s %s  (~$%.2f)\n", humanIn(q.AmountIn, p.origin.Decimals), p.origin.Symbol, p.usdIn)
	fmt.Printf("out        %.6f %s  (~$%s)\n", out, p.dest.Symbol, orDash(q.AmountOutUSD))
	fmt.Printf("min out    %s %s\n", humanIn(q.MinAmountOut, p.dest.Decimals), p.dest.Symbol)
	fmt.Printf("estimate   %.0fs\n", q.TimeEstimate)
	fmt.Printf("correlation %s\n", p.resp.CorrelationID)
	if q.DepositAddress == "" {
		fmt.Printf("deposit    —  (dry: no deposit address created, nothing can move)\n")
	} else {
		fmt.Printf("deposit    %s\n", q.DepositAddress)
	}
}

func cmdQuote(args []string) {
	fs := flag.NewFlagSet("quote", flag.ExitOnError)
	from := fs.String("from", "", "origin symbol, e.g. wNEAR")
	to := fs.String("to", "", "destination symbol, e.g. USDT")
	amount := fs.Float64("amount", 0, "amount in whole origin tokens")
	slip := fs.Int("slippage", 100, "slippage tolerance in bps")
	usd := fs.Float64("usd", 0, "USD value of the input (defaults to token price × amount)")
	asJSON := fs.Bool("json", false, "emit the raw API response as JSON")
	fs.Parse(args)
	if *from == "" || *to == "" || *amount <= 0 {
		fail(fmt.Errorf("quote needs -from, -to and -amount > 0"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, err := buildQuote(ctx, client(), *from, *to, *amount, *usd, *slip, true)
	if err != nil {
		fail(err)
	}
	if *asJSON {
		printJSON(p.resp)
		return
	}
	powder(p)
}

func cmdGuard(args []string) {
	fs := flag.NewFlagSet("guard", flag.ExitOnError)
	from := fs.String("from", "", "origin symbol")
	to := fs.String("to", "", "destination symbol")
	amount := fs.Float64("amount", 0, "amount in whole origin tokens")
	usd := fs.Float64("usd", 0, "USD value of the input")
	slip := fs.Int("slippage", 100, "slippage tolerance in bps")
	polPath := fs.String("policy", "policy.json", "policy file")
	approve := fs.Bool("approve", false, "human approval token for quotes above the threshold")
	ledgerPath := fs.String("ledger", "", "ledger file for daily spend (default: <policy dir>/ledger.jsonl)")
	asJSON := fs.Bool("json", false, "machine-readable decision")
	fs.Parse(args)
	if *from == "" || *to == "" || *amount <= 0 || *usd <= 0 {
		fail(fmt.Errorf("guard needs -from, -to, -amount and -usd (> 0)"))
	}

	pol, err := policy.Load(*polPath)
	if err != nil {
		fail(err)
	}
	if *ledgerPath == "" {
		*ledgerPath = filepath.Join(filepath.Dir(*polPath), "ledger.jsonl")
	}
	entries, err := readLedger(*ledgerPath)
	if err != nil {
		fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := client()
	p, err := buildQuote(ctx, c, *from, *to, *amount, *usd, *slip, true)
	if err != nil {
		fail(err)
	}

	view := policy.QuoteView{
		OriginAsset:      p.origin.AssetID,
		DestinationAsset: p.dest.AssetID,
		AmountInUSD:      p.usdIn,
		SlippageBps:      *slip,
	}
	dec := pol.Evaluate(view, policy.SpentToday(entries, time.Now()))

	// A policy that needs approval and a caller that did not bring an approval token
	// is not "allowed" — it is a proposal. That distinction is the whole product.
	if dec.NeedsApproval && !*approve {
		dec.Allowed = false
	}

	if *asJSON {
		printJSON(map[string]any{
			"policy":   pol.Name,
			"decision": dec,
			"quote":    p.resp.Quote,
		})
	} else {
		powder(p)
		fmt.Println()
		fmt.Printf("policy     %s\n", pol.Name)
		for _, ok := range dec.ReasonsOK {
			fmt.Printf("  ✅ %s\n", ok)
		}
		for _, r := range dec.Reasons {
			fmt.Printf("  ⛔ %s\n", r)
		}
	}

	if !dec.Allowed {
		fmt.Println("\nBLOCKED — no deposit address was created, no funds can move.")
		os.Exit(1)
	}

	// Only now, with policy pass + explicit approval, ask for a real quote.
	live, err := buildQuote(ctx, c, *from, *to, *amount, *usd, *slip, false)
	if err != nil {
		fail(fmt.Errorf("live quote: %w", err))
	}
	fmt.Printf("\nAPPROVED — deposit address created: %s\n", live.resp.Quote.DepositAddress)
	fmt.Println("funding it is the owner's action; this tool never signs a transaction.")
	if err := appendLedger(*ledgerPath, policy.LedgerEntry{
		At: time.Now().UTC(), Origin: view.OriginAsset, Dest: view.DestinationAsset,
		AmountUSD: view.AmountInUSD, Note: "approved by -approve",
	}); err != nil {
		fail(err)
	}
}

func readLedger(path string) ([]policy.LedgerEntry, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []policy.LedgerEntry
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e policy.LedgerEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // a corrupt line must not block the guard
		}
		out = append(out, e)
	}
	return out, nil
}

func appendLedger(path string, e policy.LedgerEntry) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// ---- helpers ------------------------------------------------------------

func pow10(n int) float64 {
	v := 1.0
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	return f
}

func humanIn(raw string, dec int) string {
	if raw == "" {
		return "?"
	}
	return fmt.Sprintf("%.6f", parseFloat(raw)/pow10(dec))
}

func orDash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
