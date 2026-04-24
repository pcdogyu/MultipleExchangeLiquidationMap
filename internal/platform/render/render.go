package render

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"os"
	texttemplate "text/template"

	sharedtypes "multipleexchangeliquidationmap/internal/shared/types"
)

func HTMLPage(w http.ResponseWriter, name, body string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if tpl, parseErr := template.New(name).Parse(body); parseErr == nil {
		var buf bytes.Buffer
		if execErr := tpl.Execute(&buf, data); execErr == nil {
			_, _ = w.Write(buf.Bytes())
			return
		}
	}

	if data == nil {
		_, _ = io.WriteString(w, body)
		return
	}

	tpl, err := texttemplate.New(name).Parse(body)
	if err != nil {
		http.Error(w, "page render failed", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		http.Error(w, "page render failed", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(buf.Bytes())
}

func PreferredFileOrFallback(w http.ResponseWriter, page sharedtypes.HTMLPage, data any) {
	for _, filename := range page.Preferred {
		if body, err := os.ReadFile(filename); err == nil {
			HTMLPage(w, page.TemplateName, string(body), data)
			return
		}
	}
	HTMLPage(w, page.TemplateName, page.FallbackHTML, data)
}
