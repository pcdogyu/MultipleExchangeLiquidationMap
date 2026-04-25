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
		Preferred:    []string{file("home_page_utf8.html")},
	}
}

func Analysis() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "analysis",
		FallbackHTML: liqmap.AnalysisHTMLFallback(),
		Preferred:    []string{file("analysis_page_utf8.html")},
	}
}

func Monitor() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "monitor",
		FallbackHTML: liqmap.MonitorHTML(),
		Preferred:    []string{file("monitor_page_utf8.html")},
	}
}

func Bookmap() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "map",
		FallbackHTML: liqmap.MapHTML(),
		Preferred:    []string{file("map_page_fixed.html")},
	}
}

func Liquidations() sharedtypes.HTMLPage {
	return sharedtypes.HTMLPage{
		TemplateName: "liquidations",
		FallbackHTML: `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>强平清算</title></head><body style="font-family:Segoe UI,Arial,sans-serif;padding:32px"><h1>强平清算页面</h1><p>页面文件缺失，请检查 <code>internal/shared/pages/files/liquidations_page_fixed.html</code> 是否存在。</p></body></html>`,
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
