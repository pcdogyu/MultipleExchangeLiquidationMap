package marketinfo

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/platform/httpx"
	"multipleexchangeliquidationmap/internal/platform/pageview"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

type Services interface {
	BinanceMarketInfo(symbol, period string, limit int) (liqmap.MarketInfoResponse, error)
}

type service struct {
	core Services
}

func newService(core Services) *service {
	return &service{core: core}
}

func (s *service) handlePage(w http.ResponseWriter, r *http.Request) {
	pageview.Serve(w, r, pages.MarketInfo(), nil, pageview.Options{})
}

func (s *service) handleMarketInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}

	period := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("period")))
	switch period {
	case "":
		period = "5m"
	case "5m", "15m", "30m", "1h", "2h", "4h", "6h", "12h", "1d":
	default:
		http.Error(w, "invalid period", http.StatusBadRequest)
		return
	}

	limit := 288
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 12 || n > 500 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = n
	}

	resp, err := s.core.BinanceMarketInfo(r.URL.Query().Get("symbol"), period, limit)
	if err != nil {
		var badRequest liqmap.BadRequestError
		if errors.As(err, &badRequest) {
			http.Error(w, badRequest.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
