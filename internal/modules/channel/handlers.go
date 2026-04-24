package channel

import (
	"net/http"

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
	pageview.Serve(w, r, pages.Channel(), h.deps.Core.LoadSettings(), pageview.Options{})
}
