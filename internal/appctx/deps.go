package appctx

import liqmap "multipleexchangeliquidationmap"

type Dependencies struct {
	Core  *liqmap.App
	Debug bool
}
