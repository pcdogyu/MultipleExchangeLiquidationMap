package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	liqmap "multipleexchangeliquidationmap"
)

type stubServices struct {
	cfg liqmap.ModelConfig
}

func (s *stubServices) LoadModelConfig() liqmap.ModelConfig {
	return s.cfg
}

func (s *stubServices) SaveModelConfig(req liqmap.ModelConfig) error {
	s.cfg = req
	return nil
}

func (s *stubServices) RunModelFit(hours, minEvents int, exchange, mode string) (map[string]any, error) {
	return map[string]any{"symbol": "ETHUSDT"}, nil
}

func newTestService() *service {
	return newService(&stubServices{})
}

func TestHandleModelConfigPersistsSettings(t *testing.T) {
	svc := newTestService()

	getReq := httptest.NewRequest(http.MethodGet, "/api/model-config", nil)
	getRec := httptest.NewRecorder()
	svc.handleModelConfig(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}

	var cfg liqmap.ModelConfig
	if err := json.Unmarshal(getRec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("unmarshal model config: %v", err)
	}
	cfg.LookbackMin = 180
	cfg.BucketMin = 5
	cfg.PriceStep = 8
	cfg.PriceRange = 450
	cfg.WeightCSV = "0.142857,0.142857,0.142857,0.142857,0.142857,0.142857,0.142857"
	cfg.MaintMarginCSV = "0.0050,0.0050,0.0050,0.0050,0.0050,0.0050,0.0050"
	cfg.FundingScaleCSV = "7000,7000,7000,7000,7000,7000,7000"

	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal model config: %v", err)
	}
	postReq := httptest.NewRequest(http.MethodPost, "/api/model-config", strings.NewReader(string(body)))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	svc.handleModelConfig(postRec, postReq)
	if postRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d, body=%s", postRec.Code, postRec.Body.String())
	}

	verifyReq := httptest.NewRequest(http.MethodGet, "/api/model-config", nil)
	verifyRec := httptest.NewRecorder()
	svc.handleModelConfig(verifyRec, verifyReq)
	if verifyRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", verifyRec.Code)
	}

	var updated liqmap.ModelConfig
	if err := json.Unmarshal(verifyRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal updated model config: %v", err)
	}
	if updated.LookbackMin != 180 {
		t.Fatalf("expected lookback_min 180, got %d", updated.LookbackMin)
	}
	if updated.BucketMin != 5 {
		t.Fatalf("expected bucket_min 5, got %d", updated.BucketMin)
	}
}

func TestHandleModelFitReturnsSnapshot(t *testing.T) {
	svc := newTestService()

	req := httptest.NewRequest(http.MethodGet, "/api/model-fit?hours=24&min_events=25", nil)
	rec := httptest.NewRecorder()
	svc.handleModelFit(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"symbol":"ETHUSDT"`) {
		t.Fatalf("expected symbol in response, got %s", rec.Body.String())
	}
}
