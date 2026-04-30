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
	strategy      string
	noiseStrategy string
}

func (s *captureServices) AnalysisBacktest(hours int, interval string, minConfidence float64, qualityMode string, noiseStrategy string) (liqmap.AnalysisBacktestPageResponse, error) {
	s.noiseStrategy = noiseStrategy
	return liqmap.AnalysisBacktestPageResponse{}, nil
}

func (s *captureServices) AnalysisBacktest2FA(hours int, interval string, factor string, minConfidence float64, strategy string) (liqmap.AnalysisBacktest2FAResponse, error) {
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
