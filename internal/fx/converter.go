// Package fx contains the core currency conversion logic.
//
// This is the most important file for your Atomic portfolio piece.
// It shows you understand:
//   - How FX rates + spreads work (Atomic trades across 60 global markets)
//   - Decimal precision (never use float64 for money — this is a classic fintech mistake)
//   - Idiomatic Go: structs, methods, interfaces, error wrapping
package fx

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// -------------------------------------------------------------------
// Types
// -------------------------------------------------------------------

// Currency represents an ISO 4217 currency code, e.g. "USD", "EUR", "GBP".
// Using a named type (not just string) prevents accidentally swapping
// source and target currencies — a common fintech bug.
type Currency string

const (
	USD Currency = "USD"
	EUR Currency = "EUR"
	GBP Currency = "GBP"
	CHF Currency = "CHF"
	SEK Currency = "SEK"
	NOK Currency = "NOK"
	DKK Currency = "DKK"
	PLN Currency = "PLN"
	CZK Currency = "CZK"
	HUF Currency = "HUF"
)

// Rate holds a single currency pair's exchange rate plus the spread
// Atomic charges for the conversion.
//
// Example: EUR/USD mid-market = 1.0850, spread = 0.5%
// Buy rate  = 1.0850 * (1 + 0.005) = 1.0904   (customer buys USD, pays more EUR)
// Sell rate = 1.0850 * (1 - 0.005) = 1.0796   (customer sells USD, gets less EUR)
type Rate struct {
	Base      Currency        // e.g. EUR
	Quote     Currency        // e.g. USD
	MidMarket decimal.Decimal // raw interbank rate
	Spread    decimal.Decimal // fractional spread, e.g. 0.005 = 0.5%
	FetchedAt time.Time
}

// BuyRate is the rate a customer pays when buying Quote currency.
// They pay slightly more than mid-market — this is how Atomic earns FX revenue.
func (r Rate) BuyRate() decimal.Decimal {
	return r.MidMarket.Mul(decimal.NewFromFloat(1).Add(r.Spread))
}

// SellRate is the rate a customer receives when selling Quote currency.
// They receive slightly less than mid-market.
func (r Rate) SellRate() decimal.Decimal {
	return r.MidMarket.Mul(decimal.NewFromFloat(1).Sub(r.Spread))
}

// PairKey returns a canonical string key like "EUR/USD" for map lookups.
func (r Rate) PairKey() string {
	return fmt.Sprintf("%s/%s", r.Base, r.Quote)
}

// ConversionRequest is what a caller submits to convert money.
type ConversionRequest struct {
	Amount         decimal.Decimal // how much to convert
	SourceCurrency Currency        // currency being sold
	TargetCurrency Currency        // currency being bought
	Direction      Direction       // BUY or SELL (see below)
}

// Direction tells us which side of the spread to use.
type Direction string

const (
	// Buy means the customer is acquiring TargetCurrency (pays BuyRate).
	Buy Direction = "BUY"
	// Sell means the customer is disposing of SourceCurrency (gets SellRate).
	Sell Direction = "SELL"
)

// ConversionResult is returned after a successful conversion.
type ConversionResult struct {
	Request         ConversionRequest
	AppliedRate     decimal.Decimal // the actual rate used (buy or sell side)
	MidMarketRate   decimal.Decimal // for transparency / audit trail
	ConvertedAmount decimal.Decimal // how much the customer receives
	SpreadCost      decimal.Decimal // how much the spread cost in source currency
	ConvertedAt     time.Time
}

// -------------------------------------------------------------------
// RateProvider interface
// -------------------------------------------------------------------

// RateProvider abstracts where rates come from.
// In production this would call an ECB feed or Bloomberg.
// In tests we can inject a mock — this is the "interface for testability"
// pattern you'll use throughout Go.
type RateProvider interface {
	GetRate(base, quote Currency) (Rate, error)
}

// -------------------------------------------------------------------
// Converter — the core FX engine
// -------------------------------------------------------------------

// Converter performs currency conversions using a RateProvider.
// It is the main struct you'd wire into your HTTP handlers.
type Converter struct {
	provider RateProvider
}

// NewConverter creates a Converter backed by the given provider.
func NewConverter(provider RateProvider) *Converter {
	return &Converter{provider: provider}
}

// Convert performs the currency conversion and returns a full audit-friendly result.
//
// Key decisions made here:
//  1. All arithmetic uses decimal.Decimal — never float64 for money.
//  2. We look up both direct (A→B) and inverse (B→A) rates, so you only
//     need to store one direction in your rate table.
//  3. Spread cost is calculated explicitly so it can be logged / reported.
func (c *Converter) Convert(req ConversionRequest) (ConversionResult, error) {
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return ConversionResult{}, fmt.Errorf("convert: amount must be positive, got %s", req.Amount)
	}
	if req.SourceCurrency == req.TargetCurrency {
		return ConversionResult{}, fmt.Errorf("convert: source and target currency are both %s", req.SourceCurrency)
	}

	rate, err := c.resolveRate(req.SourceCurrency, req.TargetCurrency)
	if err != nil {
		return ConversionResult{}, fmt.Errorf("convert: %w", err)
	}

	// Choose buy or sell side of the spread
	var appliedRate decimal.Decimal
	switch req.Direction {
	case Buy:
		appliedRate = rate.BuyRate()
	case Sell:
		appliedRate = rate.SellRate()
	default:
		return ConversionResult{}, fmt.Errorf("convert: unknown direction %q", req.Direction)
	}

	converted := req.Amount.Mul(appliedRate)

	// SpreadCost = what the customer would have gotten at mid-market minus what they actually got
	midConverted := req.Amount.Mul(rate.MidMarket)
	spreadCost := midConverted.Sub(converted).Abs()

	return ConversionResult{
		Request:         req,
		AppliedRate:     appliedRate,
		MidMarketRate:   rate.MidMarket,
		ConvertedAmount: converted,
		SpreadCost:      spreadCost,
		ConvertedAt:     time.Now().UTC(),
	}, nil
}

// resolveRate tries direct lookup first (e.g. EUR→USD), then inverse (USD→EUR inverted).
// This means your rate table only needs one direction per pair.
func (c *Converter) resolveRate(source, target Currency) (Rate, error) {
	// Try direct: source is base, target is quote
	if rate, err := c.provider.GetRate(source, target); err == nil {
		return rate, nil
	}

	// Try inverse: target is base, source is quote — then invert the rate
	if rate, err := c.provider.GetRate(target, source); err == nil {
		if rate.MidMarket.IsZero() {
			return Rate{}, fmt.Errorf("rate %s/%s has zero mid-market value", target, source)
		}
		one := decimal.NewFromFloat(1)
		invertedMid := one.Div(rate.MidMarket)
		return Rate{
			Base:      source,
			Quote:     target,
			MidMarket: invertedMid,
			Spread:    rate.Spread, // same spread applies
			FetchedAt: rate.FetchedAt,
		}, nil
	}

	return Rate{}, fmt.Errorf("no rate found for %s/%s (tried both directions)", source, target)
}

// -------------------------------------------------------------------
// StaticRateProvider — for development and testing
// -------------------------------------------------------------------

// StaticRateProvider holds a fixed set of rates in memory.
// In production you'd replace this with an ECBRateProvider or similar.
type StaticRateProvider struct {
	rates map[string]Rate
}

// NewStaticRateProvider creates a provider seeded with realistic EUR-base rates.
// All rates are expressed as EUR/X (how many units of X per 1 EUR).
func NewStaticRateProvider() *StaticRateProvider {
	spread := decimal.NewFromFloat(0.005) // 0.5% — typical retail FX spread

	rates := []Rate{
		{Base: EUR, Quote: USD, MidMarket: decimal.NewFromFloat(1.0850), Spread: spread},
		{Base: EUR, Quote: GBP, MidMarket: decimal.NewFromFloat(0.8560), Spread: spread},
		{Base: EUR, Quote: CHF, MidMarket: decimal.NewFromFloat(0.9780), Spread: spread},
		{Base: EUR, Quote: SEK, MidMarket: decimal.NewFromFloat(11.320), Spread: spread},
		{Base: EUR, Quote: NOK, MidMarket: decimal.NewFromFloat(11.760), Spread: spread},
		{Base: EUR, Quote: DKK, MidMarket: decimal.NewFromFloat(7.4580), Spread: spread},
		{Base: EUR, Quote: PLN, MidMarket: decimal.NewFromFloat(4.2550), Spread: spread},
		{Base: EUR, Quote: CZK, MidMarket: decimal.NewFromFloat(25.280), Spread: spread},
		{Base: EUR, Quote: HUF, MidMarket: decimal.NewFromFloat(395.50), Spread: spread},
		{Base: GBP, Quote: USD, MidMarket: decimal.NewFromFloat(1.2680), Spread: spread},
	}

	m := make(map[string]Rate, len(rates))
	for _, r := range rates {
		m[r.PairKey()] = r
	}

	now := time.Now().UTC()
	for k, v := range m {
		v.FetchedAt = now
		m[k] = v
	}

	return &StaticRateProvider{rates: m}
}

// GetRate returns the rate for a base/quote pair or an error if not found.
func (p *StaticRateProvider) GetRate(base, quote Currency) (Rate, error) {
	key := fmt.Sprintf("%s/%s", base, quote)
	rate, ok := p.rates[key]
	if !ok {
		return Rate{}, fmt.Errorf("rate not found: %s", key)
	}
	return rate, nil
}

// SupportedPairs returns all pair keys this provider knows about.
func (p *StaticRateProvider) SupportedPairs() []string {
	pairs := make([]string, 0, len(p.rates))
	for k := range p.rates {
		pairs = append(pairs, k)
	}
	return pairs
}

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

// ValidateCurrency checks whether a string is a known supported currency.
func ValidateCurrency(s string) (Currency, error) {
	c := Currency(strings.ToUpper(strings.TrimSpace(s)))
	supported := map[Currency]bool{
		USD: true, EUR: true, GBP: true, CHF: true,
		SEK: true, NOK: true, DKK: true, PLN: true,
		CZK: true, HUF: true,
	}
	if !supported[c] {
		return "", fmt.Errorf("unsupported currency: %q", s)
	}
	return c, nil
}
