package liqmap

import "testing"

func TestWebDataSourcePayloadCurrentPrice(t *testing.T) {
	payload := map[string]any{
		"lastPrice": 2338.42,
		"rangeLow":  2200.0,
		"rangeHigh": 2500.0,
	}
	if got := webDataSourcePayloadCurrentPrice(payload, 2200, 2500); got != 2338.4 {
		t.Fatalf("expected payload price 2338.4, got %v", got)
	}

	if got := webDataSourcePayloadCurrentPrice(map[string]any{}, 2200, 2500); got != 2350.0 {
		t.Fatalf("expected midpoint fallback 2350.0, got %v", got)
	}
}
