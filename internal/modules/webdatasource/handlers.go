package webdatasource

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/appctx"
)

type handlers struct {
	deps *appctx.Dependencies
}

func newHandlers(deps *appctx.Dependencies) *handlers {
	return &handlers{deps: deps}
}

func (h *handlers) handlePage(w http.ResponseWriter, r *http.Request) {
	h.deps.Core.HandleWebDataSourcePage(w, r)
}
