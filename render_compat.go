package liqmap

import (
	"net/http"

	internalrender "multipleexchangeliquidationmap/internal/platform/render"
)

func renderHTMLPage(w http.ResponseWriter, name, body string, data any) {
	internalrender.HTMLPage(w, name, body, data)
}
