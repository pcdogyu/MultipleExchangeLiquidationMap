package pages

import (
	"path/filepath"

	liqmap "multipleexchangeliquidationmap/internal/core"
	sharedtypes "multipleexchangeliquidationmap/internal/shared/types"
)

const filesDir = "internal/shared/pages/files"

func file(name string) string {
	return filepath.Join(filesDir, name)
}

func Home() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "index",
		FallbackHTML: liqmap.IndexHTML(),
	}
}

func Analysis() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "analysis",
		FallbackHTML: liqmap.AnalysisHTMLFallback(),
		Preferred:    []string{file("analysis_page_utf8.html"), file("analysis_page_fixed.html")},
	}
}

func Monitor() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "monitor",
		FallbackHTML: liqmap.MonitorHTML(),
	}
}

func Bookmap() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "map",
		FallbackHTML: liqmap.MapHTML(),
		Preferred:    []string{file("map_page_fixed.html"), file("map_page.html")},
	}
}

func Liquidations() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "liquidations",
		FallbackHTML: liqmap.LiquidationsHTML(),
		Preferred:    []string{file("liquidations_page_fixed.html")},
	}
}

func Bubbles() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "bubbles",
		FallbackHTML: liqmap.BubblesHTML(),
		Preferred:    []string{file("bubbles_page_fixed.html")},
	}
}

func Config() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "model_config_page",
		FallbackHTML: liqmap.ConfigHTML(),
		Preferred:    []string{file("config_page_fixed.html")},
	}
}

func Channel() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "channel",
		FallbackHTML: liqmap.ChannelHTMLV2(),
		Preferred:    []string{file("channel_page_utf8.html")},
	}
}

func WebDataSource() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "webdatasource",
		FallbackHTML: liqmap.WebDataSourceHTML(),
	}
}
