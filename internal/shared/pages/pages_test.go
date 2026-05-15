package pages

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestKnownPagesExposeTemplateAndPreferredFiles(t *testing.T) {
	cases := []struct {
		name         string
		templateName string
		preferredMin int
	}{
		{name: Home().TemplateName, templateName: "index", preferredMin: 1},
		{name: Analysis().TemplateName, templateName: "analysis", preferredMin: 1},
		{name: AnalysisBacktest().TemplateName, templateName: "analysis_backtest", preferredMin: 1},
		{name: AnalysisBacktestLiquidation().TemplateName, templateName: "analysis_backtest_liquidation", preferredMin: 1},
		{name: AnalysisBacktest2FA().TemplateName, templateName: "analysis_backtest_2fa", preferredMin: 1},
		{name: Monitor().TemplateName, templateName: "monitor", preferredMin: 1},
		{name: Bookmap().TemplateName, templateName: "map", preferredMin: 1},
		{name: Liquidations().TemplateName, templateName: "liquidations", preferredMin: 1},
		{name: Bubbles().TemplateName, templateName: "bubbles", preferredMin: 1},
		{name: MarketInfo().TemplateName, templateName: "market_info", preferredMin: 1},
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

func TestPreferredPageUpgradeControlsDoNotNavigateToConfig(t *testing.T) {
	pages := []struct {
		name      string
		preferred []string
	}{
		{name: Home().TemplateName, preferred: Home().Preferred},
		{name: Analysis().TemplateName, preferred: Analysis().Preferred},
		{name: AnalysisBacktest().TemplateName, preferred: AnalysisBacktest().Preferred},
		{name: AnalysisBacktestLiquidation().TemplateName, preferred: AnalysisBacktestLiquidation().Preferred},
		{name: AnalysisBacktest2FA().TemplateName, preferred: AnalysisBacktest2FA().Preferred},
		{name: Monitor().TemplateName, preferred: Monitor().Preferred},
		{name: Bookmap().TemplateName, preferred: Bookmap().Preferred},
		{name: Liquidations().TemplateName, preferred: Liquidations().Preferred},
		{name: Bubbles().TemplateName, preferred: Bubbles().Preferred},
		{name: MarketInfo().TemplateName, preferred: MarketInfo().Preferred},
		{name: Config().TemplateName, preferred: Config().Preferred},
		{name: Channel().TemplateName, preferred: Channel().Preferred},
	}
	legacyUpgradeLink := regexp.MustCompile(`<a[^>]*href="/config"[^>]*>(?:升级|&#21319;&#32423;)</a>`)

	for _, page := range pages {
		for _, filename := range page.preferred {
			body, err := os.ReadFile(filepath.Join("..", "..", "..", filename))
			if err != nil {
				t.Fatalf("%s expected preferred file %s: %v", page.name, filename, err)
			}
			if legacyUpgradeLink.Match(body) {
				t.Fatalf("%s preferred file %s has upgrade link navigating to /config", page.name, filename)
			}
		}
	}
}
