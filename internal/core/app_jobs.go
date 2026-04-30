package liqmap

import "context"

func (a *App) StartBackgroundJobs(ctx context.Context) {
	a.startCollector(ctx)
	a.startOrderBookSync(ctx)
	a.startLiquidationSync(ctx)
	a.startTelegramNotifier(ctx)
	a.startModelMapSnapshotter(ctx)
	if a.webds != nil {
		a.webds.start(ctx)
	}
}
