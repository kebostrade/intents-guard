// Package oneclick is a minimal client for the NEAR Intents 1Click Swap API.
// Docs: https://docs.near-intents.org/api-reference/oneclick
package oneclick

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://1click.chaindefuser.com"

type Token struct {
	AssetID         string  `json:"assetId"`
	Symbol          string  `json:"symbol"`
	Blockchain      string  `json:"blockchain"`
	Decimals        int     `json:"decimals"`
	Price           float64 `json:"price"`
	ContractAddress string  `json:"contractAddress,omitempty"`
}

// QuoteRequest mirrors POST /v0/quote. Only the fields this tool sets are modelled;
// unknown fields are simply absent, which the API treats as defaults.
type QuoteRequest struct {
	Dry               bool   `json:"dry"`
	SwapType          string `json:"swapType"`
	SlippageTolerance int    `json:"slippageTolerance"`
	OriginAsset       string `json:"originAsset"`
	DepositType       string `json:"depositType"`
	DestinationAsset  string `json:"destinationAsset"`
	Amount            string `json:"amount"`
	Recipient         string `json:"recipient"`
	RecipientType     string `json:"recipientType"`
	RefundTo          string `json:"refundTo"`
	RefundType        string `json:"refundType"`
	Deadline          string `json:"deadline"`
}

type AmountEstimate struct {
	AmountIn     string `json:"amountIn"`
	AmountInUSD  string `json:"amountInUsd,omitempty"`
	AmountOut    string `json:"amountOut"`
	AmountOutUSD string `json:"amountOutUsd,omitempty"`
	MinAmountOut string `json:"minAmountOut,omitempty"`
}

type Quote struct {
	DepositAddress     string  `json:"depositAddress"`
	AmountIn           string  `json:"amountIn"`
	AmountInFormatted  string  `json:"amountInFormatted,omitempty"`
	AmountInUSD        string  `json:"amountInUsd,omitempty"`
	AmountOut          string  `json:"amountOut"`
	AmountOutFormatted string  `json:"amountOutFormatted,omitempty"`
	AmountOutUSD       string  `json:"amountOutUsd,omitempty"`
	MinAmountOut       string  `json:"minAmountOut,omitempty"`
	TimeEstimate       float64 `json:"timeEstimate"`
	Deadline           string  `json:"deadline,omitempty"`
}

type QuoteResponse struct {
	CorrelationID string `json:"correlationId"`
	Timestamp     string `json:"timestamp"`
	Signature     string `json:"signature"`
	Quote         Quote  `json:"quote"`
}

type Client struct {
	BaseURL string
	APIKey  string // optional partner JWT; unauthenticated quotes carry a +25 bps fee
	HC      *http.Client
}

func New(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey,
		HC: &http.Client{Timeout: 45 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("X-API-Key", c.APIKey)
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: http %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: bad json: %w", method, path, err)
	}
	return nil
}

// Tokens lists every asset the intent solver network can route.
func (c *Client) Tokens(ctx context.Context) ([]Token, error) {
	var out []Token
	err := c.do(ctx, http.MethodGet, "/v0/tokens", nil, &out)
	return out, err
}

// Quote asks for a swap quote. dry=true previews without creating a deposit address.
func (c *Client) Quote(ctx context.Context, q QuoteRequest) (*QuoteResponse, error) {
	var out QuoteResponse
	if err := c.do(ctx, http.MethodPost, "/v0/quote", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status polls the execution status of a deposit address.
func (c *Client) Status(ctx context.Context, depositAddress string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, http.MethodGet, "/v0/status?depositAddress="+depositAddress, nil, &out)
	return out, err
}
