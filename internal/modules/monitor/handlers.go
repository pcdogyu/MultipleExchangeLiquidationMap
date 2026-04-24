package monitor

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
	if r.URL.Query().Get("days") == "" {
		q := r.URL.Query()
		q.Set("days", "30")
		http.Redirect(w, r, r.URL.Path+"?"+q.Encode(), http.StatusFound)
		return
	}
	render.PreferredFileOrFallback(w, pages.Monitor(), nil)
}
