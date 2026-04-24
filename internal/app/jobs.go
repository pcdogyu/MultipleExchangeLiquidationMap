package app

import "context"

func StartJobs(ctx context.Context, deps *Dependencies) {
	deps.Core.StartBackgroundJobs(ctx)
}
