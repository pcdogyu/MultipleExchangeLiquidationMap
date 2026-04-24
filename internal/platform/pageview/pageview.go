package pageview

import (
	"net/http"

	"multipleexchangeliquidationmap/internal/platform/httpx"
	"multipleexchangeliquidationmap/internal/platform/render"
	sharedtypes "multipleexchangeliquidationmap/internal/shared/types"
)

type Options struct {
	ExactPath    string
	NoStore      bool
	DefaultQuery map[string]string
}

func Serve(w http.ResponseWriter, r *http.Request, page sharedtypes.HTMLPage, data any, opts Options) {
	if opts.ExactPath != "" && r.URL.Path != opts.ExactPath {
		http.NotFound(w, r)
		return
	}

	if len(opts.DefaultQuery) > 0 {
		q := r.URL.Query()
		changed := false
		for key, value := range opts.DefaultQuery {
			if q.Get(key) == "" {
				q.Set(key, value)
				changed = true
			}
		}
		if changed {
			http.Redirect(w, r, r.URL.Path+"?"+q.Encode(), http.StatusFound)
			return
		}
	}

	if opts.NoStore {
		httpx.NoStore(w)
	}

	render.PreferredFileOrFallback(w, page, data)
}
