package channel

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
	"multipleexchangeliquidationmap/internal/platform/render"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

type handlers struct {
	deps *appctx.Dependencies
}

func newHandlers(deps *appctx.Dependencies) *handlers {
	return &handlers{deps: deps}
}

func (h *handlers) handlePage(w http.ResponseWriter, r *http.Request) {
	render.PreferredFileOrFallback(w, pages.Channel(), h.deps.Core.LoadSettings())
}
