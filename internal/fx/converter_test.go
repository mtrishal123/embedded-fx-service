package fx

import (
	"testing"

	"github.com/shopspring/decimal"
)

// -------------------------------------------------------------------
// The float64 precision trap
// -------------------------------------------------------------------

// TestFloat64IsUnsafeForMoney documents *why* this whole package uses
// decimal.Decimal instead of float64. This is the single most important
// lesson in financial software: 0.1 + 0.2 != 0.3 in floating point.
func TestFloat64IsUnsafeForMoney(t *testing.T) {
	// Floating point: the classic failure.
	//
	// Note: these MUST be runtime float64 variables. Go evaluates untyped
	// constant expressions like `0.1 + 0.2` with arbitrary precision at
	// compile time, so writing it as a constant would hide the very bug
	// we're demonstrating. Forcing float64 vars reproduces real arithmetic.
	var a, b, want float64 = 0.1, 0.2, 0.3
	got := a + b
	if got == want {
		t.Fatal("expected 0.1 + 0.2 != 0.3 with float64 — the trap didn't trigger")
	}
	t.Logf("float64: 0.1 + 0.2 = %.17f (NOT exactly 0.3) — this is why we use decimal", got)

	// Decimal: the fix.
	da := decimal.NewFromFloat(0.1)
	db := decimal.NewFromFloat(0.2)
	sum := da.Add(db)
	if !sum.Equal(decimal.NewFromFloat(0.3)) {
		t.Fatalf("decimal: expected 0.1 + 0.2 == 0.3, got %s", sum)
	}
}

// -------------------------------------------------------------------
// Conversion logic
// -------------------------------------------------------------------

func newTestConverter() *Converter {
	return NewConverter(NewStaticRateProvider())
}

func TestConvert_BuySide(t *testing.T) {
	c := newTestConverter()

	// Buy USD with EUR. Mid = 1.0850, spread 0.5% -> buy rate 1.0850 * 1.005.
	req := ConversionRequest{
		Amount:         decimal.NewFromInt(100),
		SourceCurrency: EUR,
		TargetCurrency: USD,
		Direction:      Buy,
	}

	res, err := c.Convert(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantRate := decimal.NewFromFloat(1.0850).Mul(decimal.NewFromFloat(1.005))
	if !res.AppliedRate.Equal(wantRate) {
		t.Errorf("applied rate: want %s, got %s", wantRate, res.AppliedRate)
	}

	wantAmount := decimal.NewFromInt(100).Mul(wantRate)
	if !res.ConvertedAmount.Equal(wantAmount) {
		t.Errorf("converted amount: want %s, got %s", wantAmount, res.ConvertedAmount)
	}

	// On the buy side the customer pays more than mid-market, so spread cost > 0.
	if !res.SpreadCost.GreaterThan(decimal.Zero) {
		t.Errorf("expected positive spread cost, got %s", res.SpreadCost)
	}
}

func TestConvert_SellSide(t *testing.T) {
	c := newTestConverter()

	req := ConversionRequest{
		Amount:         decimal.NewFromInt(100),
		SourceCurrency: EUR,
		TargetCurrency: USD,
		Direction:      Sell,
	}

	res, err := c.Convert(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantRate := decimal.NewFromFloat(1.0850).Mul(decimal.NewFromFloat(0.995))
	if !res.AppliedRate.Equal(wantRate) {
		t.Errorf("applied rate: want %s, got %s", wantRate, res.AppliedRate)
	}
}

// TestConvert_InverseRate proves the resolveRate fallback works: we only store
// EUR/USD, but a USD->EUR request should still succeed by inverting.
func TestConvert_InverseRate(t *testing.T) {
	c := newTestConverter()

	req := ConversionRequest{
		Amount:         decimal.NewFromInt(100),
		SourceCurrency: USD, // not stored as a base currency
		TargetCurrency: EUR,
		Direction:      Buy,
	}

	res, err := c.Convert(req)
	if err != nil {
		t.Fatalf("inverse conversion failed: %v", err)
	}
	if !res.ConvertedAmount.GreaterThan(decimal.Zero) {
		t.Errorf("expected positive converted amount, got %s", res.ConvertedAmount)
	}
}

func TestConvert_Errors(t *testing.T) {
	c := newTestConverter()

	tests := []struct {
		name string
		req  ConversionRequest
	}{
		{
			name: "zero amount",
			req: ConversionRequest{
				Amount: decimal.Zero, SourceCurrency: EUR, TargetCurrency: USD, Direction: Buy,
			},
		},
		{
			name: "negative amount",
			req: ConversionRequest{
				Amount: decimal.NewFromInt(-5), SourceCurrency: EUR, TargetCurrency: USD, Direction: Buy,
			},
		},
		{
			name: "same currency",
			req: ConversionRequest{
				Amount: decimal.NewFromInt(10), SourceCurrency: EUR, TargetCurrency: EUR, Direction: Buy,
			},
		},
		{
			name: "unknown direction",
			req: ConversionRequest{
				Amount: decimal.NewFromInt(10), SourceCurrency: EUR, TargetCurrency: USD, Direction: "SIDEWAYS",
			},
		},
		{
			name: "no rate available",
			req: ConversionRequest{
				Amount: decimal.NewFromInt(10), SourceCurrency: SEK, TargetCurrency: HUF, Direction: Buy,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := c.Convert(tt.req); err == nil {
				t.Errorf("expected an error for %q, got nil", tt.name)
			}
		})
	}
}

func TestValidateCurrency(t *testing.T) {
	if _, err := ValidateCurrency("eur"); err != nil {
		t.Errorf("lowercase eur should be valid: %v", err)
	}
	if _, err := ValidateCurrency("  GBP  "); err != nil {
		t.Errorf("padded GBP should be valid: %v", err)
	}
	if _, err := ValidateCurrency("XYZ"); err == nil {
		t.Error("XYZ should be rejected")
	}
}
