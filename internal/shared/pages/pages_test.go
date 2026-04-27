package pages

import "testing"

func TestKnownPagesExposeTemplateAndPreferredFiles(t *testing.T) {
	cases := []struct {
		name         string
		templateName string
		preferredMin int
	}{
		{name: Home().TemplateName, templateName: "index", preferredMin: 1},
		{name: Analysis().TemplateName, templateName: "analysis", preferredMin: 1},
		{name: Monitor().TemplateName, templateName: "monitor", preferredMin: 1},
		{name: Bookmap().TemplateName, templateName: "map", preferredMin: 1},
		{name: Liquidations().TemplateName, templateName: "liquidations", preferredMin: 1},
		{name: Bubbles().TemplateName, templateName: "bubbles", preferredMin: 1},
		{name: Config().TemplateName, templateName: "model_config_page", preferredMin: 1},
		{name: Channel().TemplateName, templateName: "channel", preferredMin: 1},
	}

	for _, tc := range cases {
		if tc.name != tc.templateName {
			t.Fatalf("expected template %q, got %q", tc.templateName, tc.name)
		}
	}

	if got := len(Home().Preferred); got < 1 {
		t.Fatalf("expected Home preferred files, got %d", got)
	}
	if got := len(Liquidations().Preferred); got < 1 {
		t.Fatalf("expected Liquidations preferred files, got %d", got)
	}
}
