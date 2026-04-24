package adapters

import (
	liqmap "multipleexchangeliquidationmap/internal/core"
	"multipleexchangeliquidationmap/internal/modules/bubbles"
)

type bubblesModuleAdapter struct {
	app *liqmap.App
}

func NewBubbles(app *liqmap.App) bubbles.Services {
	return &bubblesModuleAdapter{app: app}
}

func (s *bubblesModuleAdapter) FetchKlines(interval string, limit int, startTS, endTS int64) (map[string]any, error) {
	return s.app.FetchKlines(interval, limit, startTS, endTS)
}

func (s *bubblesModuleAdapter) LatestOKXClose() (map[string]any, error) {
	return s.app.LatestOKXClose()
}
