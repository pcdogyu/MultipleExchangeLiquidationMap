package app

import liqmap "multipleexchangeliquidationmap"

import "context"

func StartJobs(ctx context.Context, core *liqmap.App) {
	core.StartBackgroundJobs(ctx)
}
