package bubbles

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
		TemplateName: "bubbles",
		FallbackHTML: liqmap.BubblesHTML(),
		Preferred:    []string{"bubbles_page_fixed.html"},
	}, nil)
}
