package bookmap

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/platform/pageview"
	"multipleexchangeliquidationmap/internal/shared/pages"
)

func handlePage(w http.ResponseWriter, r *http.Request) {
	pageview.Serve(w, r, pages.Bookmap(), nil, pageview.Options{})
}
