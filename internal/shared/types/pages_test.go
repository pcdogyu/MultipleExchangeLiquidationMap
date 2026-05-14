package types

import "testing"

func TestHTMLPageCarriesFields(t *testing.T) {
	page := HTMLPage{
		TemplateName: "demo",
		FallbackHTML: "<p>fallback</p>",
		Preferred:    []string{"a.html", "b.html"},
	}

	if page.TemplateName != "demo" {
		t.Fatalf("expected template name to round-trip, got %q", page.TemplateName)
	}
	if len(page.Preferred) != 2 {
		t.Fatalf("expected preferred file count 2, got %d", len(page.Preferred))
	}
}
