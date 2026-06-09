package liqmap

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestRetryBybitLiquidationBatchLogsSuccessAfterRetry(t *testing.T) {
	app := &App{debug: true}
	var calls int
	var logBuf bytes.Buffer
	restore := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() {
		log.SetOutput(restore)
	})

	err := app.retryBybitLiquidationBatch(context.Background(), "linear", []string{"allLiquidation.ETHUSDT"}, func(retryAttempt int) error {
		calls++
		if calls < 3 {
			return errors.New("read tcp 10.0.0.1:12345->1.2.3.4:443: i/o timeout")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryBybitLiquidationBatch returned error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	got := logBuf.String()
	for _, want := range []string{
		"bybit batched liquidation ws retry 1/3 starting",
		"bybit batched liquidation ws retry 2/3 starting",
		"bybit batched liquidation ws retry succeeded on attempt 2/3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got:\n%s", want, got)
		}
	}
}

func TestRetryBybitLiquidationBatchLogsFailureAfterExhaustedRetries(t *testing.T) {
	app := &App{debug: true}
	var calls int
	var logBuf bytes.Buffer
	restore := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() {
		log.SetOutput(restore)
	})

	wantErr := errors.New("read tcp 10.0.0.1:12345->1.2.3.4:443: i/o timeout")
	err := app.retryBybitLiquidationBatch(context.Background(), "inverse", []string{"allLiquidation.BTCUSD"}, func(retryAttempt int) error {
		calls++
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if calls != bybitBatchRetryAttempts+1 {
		t.Fatalf("calls = %d, want %d", calls, bybitBatchRetryAttempts+1)
	}
	got := logBuf.String()
	for _, want := range []string{
		"bybit batched liquidation ws retry 1/3 starting",
		"bybit batched liquidation ws retry 2/3 starting",
		"bybit batched liquidation ws retry 3/3 starting",
		"bybit batched liquidation ws retry failed after 3 attempts",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got:\n%s", want, got)
		}
	}
}
