package console

import (
	"testing"
	"time"
)

// A zero limit means the API returned no limit for that window, not that the
// budget is spent. Reporting exhaustion there lit up "LIMIT REACHED" on an
// empty meter.
func TestMeterExhaustedRequiresALimit(t *testing.T) {
	tests := []struct {
		name string
		m    Meter
		want bool
	}{
		{name: "no data at all", m: Meter{}, want: false},
		{name: "used but no limit", m: Meter{UsedMicroCents: 500}, want: false},
		{name: "limit with no spend", m: Meter{LimitMicroCents: 1000}, want: false},
		{name: "half spent", m: Meter{LimitMicroCents: 1000, UsedMicroCents: 500}, want: false},
		{name: "exactly at limit", m: Meter{LimitMicroCents: 1000, UsedMicroCents: 1000}, want: true},
		{name: "over limit", m: Meter{LimitMicroCents: 1000, UsedMicroCents: 1500}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.m.Exhausted(); got != tt.want {
				t.Errorf("Exhausted() = %v, want %v", got, tt.want)
			}
			if got := tt.m.HasLimit(); got != (tt.m.LimitMicroCents > 0) {
				t.Errorf("HasLimit() = %v for limit %d", got, tt.m.LimitMicroCents)
			}
		})
	}
}

func TestMicroCentsRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		usd  float64
		want MicroCents
	}{
		{name: "zero", usd: 0, want: 0},
		{name: "twelve dollars", usd: 12, want: 1_200_000_000},
		{name: "negative", usd: -2.5, want: -250_000_000},
		{name: "fractional", usd: 0.00000001, want: 1},
		{name: "typical rate", usd: 0.00175306, want: 175306},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FromUSD(tt.usd)
			if got != tt.want {
				t.Errorf("FromUSD(%v) = %d, want %d", tt.usd, got, tt.want)
			}
			// Rounding must not lose the sign on the way back.
			if back := got.USD(); (tt.usd < 0) != (back < 0) {
				t.Errorf("sign lost: FromUSD(%v) -> USD() = %v", tt.usd, back)
			}
		})
	}
}

func TestPrepareZeroExpiryDoesNotLoop(t *testing.T) {
	// A credential with no expiry must never be considered expired, or Prepare
	// would try to refresh on every call.
	if (Credential{}).Expired(time.Now()) {
		t.Fatal("zero expiry must not report expired")
	}
}
