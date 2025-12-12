package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// priceCacheTTL controls how often we refresh fiat prices from CoinGecko.
const priceCacheTTL = 30 * time.Minute

type PriceService struct {
	mu        sync.Mutex
	lastFetch time.Time
	lastPrice float64
	lastFiat  string
	lastErr   error
	client    *http.Client
}

func NewPriceService() *PriceService {
	return &PriceService{
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// BTCPrice returns the BTC price in the given fiat currency (e.g. "usd"),
// using a small in-process cache backed by api.coingecko.com. On errors it
// returns 0 and the error; callers should treat this as "no price available"
// and avoid failing the UI.
func (p *PriceService) BTCPrice(fiat string) (float64, error) {
	if p == nil {
		return 0, fmt.Errorf("price service not initialized")
	}
	fiat = strings.ToLower(strings.TrimSpace(fiat))
	if fiat == "" {
		fiat = "usd"
	}

	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.lastFiat == fiat && !p.lastFetch.IsZero() && now.Sub(p.lastFetch) < priceCacheTTL && p.lastPrice > 0 && p.lastErr == nil {
		return p.lastPrice, nil
	}

	url := fmt.Sprintf("https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=%s", fiat)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		p.lastErr = err
		return 0, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		p.lastErr = err
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.lastErr = fmt.Errorf("price http status %s", resp.Status)
		return 0, p.lastErr
	}

	var body map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		p.lastErr = err
		return 0, err
	}
	btc, ok := body["bitcoin"]
	if !ok {
		p.lastErr = fmt.Errorf("price response missing bitcoin key")
		return 0, p.lastErr
	}
	price, ok := btc[fiat]
	if !ok {
		p.lastErr = fmt.Errorf("price response missing %s key", fiat)
		return 0, p.lastErr
	}

	p.lastFetch = now
	p.lastPrice = price
	p.lastFiat = fiat
	p.lastErr = nil
	return price, nil
}

// LastUpdate returns the time the price was last fetched from CoinGecko.
// If no successful fetch has occurred yet, it returns the zero time.
func (p *PriceService) LastUpdate() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastFetch
}
