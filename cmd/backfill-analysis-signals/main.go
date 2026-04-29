package main

import (
	"database/sql"
	"flag"
	"log"

	"multipleexchangeliquidationmap/internal/core"
	dbplatform "multipleexchangeliquidationmap/internal/platform/db"

	_ "modernc.org/sqlite"
)

func main() {
	hours := flag.Int("hours", 24, "lookback window in hours")
	flag.Parse()

	dbPath := liqmap.Getenv("DB_PATH", liqmap.DefaultDBPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := dbplatform.Configure(db); err != nil {
		log.Fatal(err)
	}
	if err := dbplatform.Init(db); err != nil {
		log.Fatal(err)
	}

	app := liqmap.NewApp(db, true)
	count, err := app.BackfillAnalysisDirectionSignals(*hours)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("backfilled analysis_direction_signals: %d row(s) for last %d hour(s)", count, *hours)
}
