package config

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
	"multipleexchangeliquidationmap/internal/platform/httpx"
	"multipleexchangeliquidationmap/internal/platform/render"
	sharedtypes "multipleexchangeliquidationmap/internal/shared/types"
)

type handlers struct {
	deps *appctx.Dependencies
}

func newHandlers(deps *appctx.Dependencies) *handlers {
	return &handlers{deps: deps}
}

func (h *handlers) handlePage(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	render.PreferredFileOrFallback(w, sharedtypes.HTMLPage{
		TemplateName: "model_config_page",
		FallbackHTML: liqmap.ConfigHTML(),
		Preferred:    []string{"config_page_fixed.html"},
	}, h.deps.Core.BuildModelConfigPageData("config"))
}
