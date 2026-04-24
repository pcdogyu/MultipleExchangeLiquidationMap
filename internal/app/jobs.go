package app

import (
	"context"

	"multipleexchangeliquidationmap/internal/appctx"
)

func StartJobs(ctx context.Context, deps *appctx.Dependencies) {
	deps.Core.StartBackgroundJobs(ctx)
}
