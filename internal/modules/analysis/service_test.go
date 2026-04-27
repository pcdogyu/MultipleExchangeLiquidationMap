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

func (stubServices) AnalysisBacktest(hours int, minConfidence float64, horizons []int) (liqmap.AnalysisBacktestPageResponse, error) {
	return liqmap.AnalysisBacktestPageResponse{}, nil
}

func (stubServices) AnalysisBacktestHistory(limit, page int) (liqmap.AnalysisBacktestHistoryResponse, error) {
	return liqmap.AnalysisBacktestHistoryResponse{}, nil
}

func newTestService() *service {
	return newService(stubServices{})
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
