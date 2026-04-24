package liquidations

import (
	"net/http"

	liqmap "multipleexchangeliquidationmap"
	"multipleexchangeliquidationmap/internal/appctx"
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
	render.PreferredFileOrFallback(w, sharedtypes.HTMLPage{
		TemplateName: "liquidations",
		FallbackHTML: liqmap.LiquidationsHTML(),
		Preferred:    []string{"liquidations_page_fixed.html"},
	}, nil)
}
