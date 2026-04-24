package config

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
	"multipleexchangeliquidationmap/internal/platform/pageview"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

type handlers struct {
	deps *appctx.Dependencies
}

func newHandlers(deps *appctx.Dependencies) *handlers {
	return &handlers{deps: deps}
}

func (h *handlers) handlePage(w http.ResponseWriter, r *http.Request) {
	data := &liqmap.ModelConfigPageData{
		ModelConfig:      h.deps.Core.LoadModelConfig(),
		PageTitle:        "\u6a21\u578b\u914d\u7f6e",
		ActiveMenu:       "config",
		ShowAnalysisInfo: false,
	}
	pageview.Serve(w, r, pages.Config(), data, pageview.Options{
		NoStore: true,
	})
}
