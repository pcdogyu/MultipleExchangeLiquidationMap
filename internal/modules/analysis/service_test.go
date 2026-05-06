package analysis

import (
	"net/http"
	"net/http/httptest"
	"testing"

	liqmap "multipleexchangeliquidationmap/internal/core"
)

type stubServices struct{}

func (stubServices) AnalysisSnapshot() (liqmap.AnalysisSnapshot, error) {
	return liqmap.AnalysisSnapshot{}, nil
}

func (stubServices) AnalysisBacktest(hours int, interval string, minConfidence float64, qualityMode string, noiseStrategy string) (liqmap.AnalysisBacktestPageResponse, error) {
	return liqmap.AnalysisBacktestPageResponse{}, nil
}

func (stubServices) AnalysisBacktestLiquidation(hours int, interval string, minConfidence float64) (liqmap.AnalysisBacktestPageResponse, error) {
	return liqmap.AnalysisBacktestPageResponse{}, nil
}

func (stubServices) AnalysisBacktestLiquidationSignalBackfill(hours int) (liqmap.AnalysisBacktestLiquidationSignalMutationResponse, error) {
	return liqmap.AnalysisBacktestLiquidationSignalMutationResponse{}, nil
}

func (stubServices) AnalysisBacktestLiquidationSignalReset(hours int) (liqmap.AnalysisBacktestLiquidationSignalMutationResponse, error) {
	return liqmap.AnalysisBacktestLiquidationSignalMutationResponse{}, nil
}

func (stubServices) AnalysisBacktest2FA(hours int, interval string, factor string, minConfidence float64, strategy string) (liqmap.AnalysisBacktest2FAResponse, error) {
	return liqmap.AnalysisBacktest2FAResponse{}, nil
}

func (stubServices) AnalysisBacktestHistory(limit, page int) (liqmap.AnalysisBacktestHistoryResponse, error) {
	return liqmap.AnalysisBacktestHistoryResponse{}, nil
}

func newTestService() *service {
	return newService(stubServices{})
}

type captureServices struct {
	stubServices
	hours            int
	liquidationHours int
	backfillHours    int
	resetHours       int
	strategy         string
	noiseStrategy    string
}

func (s *captureServices) AnalysisBacktest(hours int, interval string, minConfidence float64, qualityMode string, noiseStrategy string) (liqmap.AnalysisBacktestPageResponse, error) {
	s.hours = hours
	s.noiseStrategy = noiseStrategy
	return liqmap.AnalysisBacktestPageResponse{}, nil
}

func (s *captureServices) AnalysisBacktestLiquidation(hours int, interval string, minConfidence float64) (liqmap.AnalysisBacktestPageResponse, error) {
	s.liquidationHours = hours
	return liqmap.AnalysisBacktestPageResponse{}, nil
}

func (s *captureServices) AnalysisBacktestLiquidationSignalBackfill(hours int) (liqmap.AnalysisBacktestLiquidationSignalMutationResponse, error) {
	s.backfillHours = hours
	return liqmap.AnalysisBacktestLiquidationSignalMutationResponse{Inserted: 2, Updated: 1, Total: 3}, nil
}

func (s *captureServices) AnalysisBacktestLiquidationSignalReset(hours int) (liqmap.AnalysisBacktestLiquidationSignalMutationResponse, error) {
	s.resetHours = hours
	return liqmap.AnalysisBacktestLiquidationSignalMutationResponse{Deleted: 4, Inserted: 2, Total: 2}, nil
}

func (s *captureServices) AnalysisBacktest2FA(hours int, interval string, factor string, minConfidence float64, strategy string) (liqmap.AnalysisBacktest2FAResponse, error) {
	s.hours = hours
	s.strategy = strategy
	return liqmap.AnalysisBacktest2FAResponse{}, nil
}

func TestHandleAnalysisRejectsWrongMethod(t *testing.T) {
	svc := newTestService()

	req := httptest.NewRequest(http.MethodPost, "/api/analysis", nil)
	rec := httptest.NewRecorder()
	svc.handleAnalysis(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleBacktestRejectsWrongMethod(t *testing.T) {
	svc := newTestService()

	req := httptest.NewRequest(http.MethodPost, "/api/analysis-backtest", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktest(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleBacktestLiquidationRejectsWrongMethod(t *testing.T) {
	svc := newTestService()

	req := httptest.NewRequest(http.MethodPost, "/api/analysis-backtest-liquidation", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktestLiquidation(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleBacktestLiquidationSignalBackfillRejectsWrongMethod(t *testing.T) {
	svc := newTestService()

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest-liquidation/signals/backfill", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktestLiquidationSignalBackfill(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleBacktestLiquidationSignalResetRejectsWrongMethod(t *testing.T) {
	svc := newTestService()

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest-liquidation/signals/reset", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktestLiquidationSignalReset(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleBacktestLiquidationSignalBackfillDefaultsToTwoWeeks(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodPost, "/api/analysis-backtest-liquidation/signals/backfill", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktestLiquidationSignalBackfill(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.backfillHours != 24*14 {
		t.Fatalf("hours = %d, want %d", core.backfillHours, 24*14)
	}
}

func TestHandleBacktestLiquidationSignalResetAcceptsTwoWeekWindow(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodPost, "/api/analysis-backtest-liquidation/signals/reset?hours=336", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktestLiquidationSignalReset(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.resetHours != 336 {
		t.Fatalf("hours = %d, want 336", core.resetHours)
	}
}

func TestHandleBacktest2FAPassesStrategy(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest-2fa?strategy=preferred", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktest2FA(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.strategy != "preferred" {
		t.Fatalf("strategy = %q, want preferred", core.strategy)
	}
}

func TestHandleBacktest2FADefaultStrategyIsEmptyForCompatibility(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest-2fa", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktest2FA(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.strategy != "" {
		t.Fatalf("strategy = %q, want empty default", core.strategy)
	}
}

func TestHandleBacktest2FADefaultsToTwoWeeks(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest-2fa", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktest2FA(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.hours != 24*14 {
		t.Fatalf("hours = %d, want %d", core.hours, 24*14)
	}
}

func TestHandleBacktest2FAAcceptsTwoWeekWindow(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest-2fa?hours=336", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktest2FA(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.hours != 336 {
		t.Fatalf("hours = %d, want 336", core.hours)
	}
}

func TestHandleBacktestDefaultsToTwoWeeks(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.hours != 24*14 {
		t.Fatalf("hours = %d, want %d", core.hours, 24*14)
	}
}

func TestHandleBacktestLiquidationDefaultsToTwoWeeks(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest-liquidation", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktestLiquidation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.liquidationHours != 24*14 {
		t.Fatalf("hours = %d, want %d", core.liquidationHours, 24*14)
	}
}

func TestHandleBacktestLiquidationAcceptsTwoWeekWindow(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest-liquidation?hours=336", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktestLiquidation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.liquidationHours != 336 {
		t.Fatalf("hours = %d, want 336", core.liquidationHours)
	}
}

func TestHandleBacktestPassesNoiseStrategy(t *testing.T) {
	core := &captureServices{}
	svc := newService(core)

	req := httptest.NewRequest(http.MethodGet, "/api/analysis-backtest?noise_strategy=persistence", nil)
	rec := httptest.NewRecorder()
	svc.handleBacktest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if core.noiseStrategy != "persistence" {
		t.Fatalf("noise strategy = %q, want persistence", core.noiseStrategy)
	}
}
