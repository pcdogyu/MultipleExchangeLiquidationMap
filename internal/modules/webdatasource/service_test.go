package webdatasource

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	liqmap "multipleexchangeliquidationmap"
)

type stubServices struct{}

func (stubServices) WebDataSourceStatus() liqmap.WebDataSourceStatus {
	return liqmap.WebDataSourceStatus{}
}

func (stubServices) TriggerWebDataSourceInit() (bool, error) {
	return true, nil
}

func (stubServices) WebDataSourceInitLoginTimeoutSec() int {
	return 90
}

func (stubServices) TriggerWebDataSourceRun(windowDays *int) (bool, error) {
	return true, nil
}

func (stubServices) ListRecentWebDataSourceRuns(limit int) []liqmap.WebDataSourceRunRow {
	return nil
}

func (stubServices) UpdateWebDataSourceSettings(enabled *bool, intervalMin, timeoutSec int, chromePath, profileDir string) liqmap.WebDataSourceStatus {
	return liqmap.WebDataSourceStatus{}
}

func (stubServices) WebDataSourceMap(window string) liqmap.WebDataSourceMapResponse {
	return liqmap.WebDataSourceMapResponse{}
}

func newTestService() *service {
	return newService(stubServices{})
}

func TestHandleStatusRejectsWrongMethod(t *testing.T) {
	svc := newTestService()

	req := httptest.NewRequest(http.MethodPost, "/api/webdatasource/status", nil)
	rec := httptest.NewRecorder()
	svc.handleStatus(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleSettingsRejectsBadJSON(t *testing.T) {
	svc := newTestService()

	req := httptest.NewRequest(http.MethodPost, "/api/webdatasource/settings", strings.NewReader("{"))
	rec := httptest.NewRecorder()
	svc.handleSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
