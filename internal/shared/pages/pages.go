package pages

import (
	liqmap "multipleexchangeliquidationmap/internal/core"
	sharedtypes "multipleexchangeliquidationmap/internal/shared/types"
)

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
		Preferred:    []string{"analysis_page_fixed.html"},
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
		Preferred:    []string{"map_page_fixed.html", "map_page.html"},
	}
}

func Liquidations() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "liquidations",
		FallbackHTML: liqmap.LiquidationsHTML(),
		Preferred:    []string{"liquidations_page_fixed.html"},
	}
}

func Bubbles() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "bubbles",
		FallbackHTML: liqmap.BubblesHTML(),
		Preferred:    []string{"bubbles_page_fixed.html"},
	}
}

func Config() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "model_config_page",
		FallbackHTML: liqmap.ConfigHTML(),
		Preferred:    []string{"config_page_fixed.html"},
	}
}

func Channel() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "channel",
		FallbackHTML: liqmap.ChannelHTMLV2(),
	}
}

func WebDataSource() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "webdatasource",
		FallbackHTML: liqmap.WebDataSourceHTML(),
	}
}
