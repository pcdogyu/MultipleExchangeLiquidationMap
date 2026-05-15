//go:build ignore
// +build ignore

package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "liqmap.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Test the query logic from handleModelFit
	hours := 24
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()

	// Query like in handleModelFit
	rows, err := db.Query(`SELECT LOWER(exchange), price, mark_price, notional_usd
		FROM liquidation_events
		WHERE symbol=? AND (event_ts>=? OR inserted_ts>=?)`, "ETHUSDT", cutoff, cutoff)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	byEx := make(map[string]int)
	totalCnt := 0
	validCnt := 0
	for rows.Next() {
		var ex string
		var price, mark, notional float64
		if err := rows.Scan(&ex, &price, &mark, &notional); err != nil {
			continue
		}
		totalCnt++

		// Check the filtering logic
		if ex == "" || !(price > 0) || !(mark > 0) || !(notional > 0) {
			continue
		}
		validCnt++
		byEx[ex] = byEx[ex] + 1
	}

	fmt.Printf("Total records in last %d hours: %d\n", hours, totalCnt)
	fmt.Printf("Valid records (price>0, mark>0, notional>0): %d\n", validCnt)
	for ex, count := range byEx {
		fmt.Printf("  %s: %d\n", ex, count)
	}

	// Check for OKX specifically
	rows3, err := db.Query(`SELECT COUNT(*) FROM liquidation_events WHERE symbol=? AND LOWER(exchange)='okx'`, "ETHUSDT")
	if err == nil {
		var okxCount int
		if rows3.Next() {
			rows3.Scan(&okxCount)
		}
		rows3.Close()
		fmt.Printf("Total OKX records in database: %d\n", okxCount)
	}

	// Check OKX timestamps
	rows4, err := db.Query(`SELECT MIN(event_ts), MAX(event_ts), MIN(inserted_ts), MAX(inserted_ts) FROM liquidation_events WHERE symbol=? AND LOWER(exchange)='okx'`, "ETHUSDT")
	if err == nil {
		var minEvent, maxEvent, minInsert, maxInsert int64
		if rows4.Next() {
			rows4.Scan(&minEvent, &maxEvent, &minInsert, &maxInsert)
			fmt.Printf("OKX timestamp range:\n")
			fmt.Printf("  event_ts: %s to %s\n",
				time.UnixMilli(minEvent).Format("2006-01-02 15:04:05"),
				time.UnixMilli(maxEvent).Format("2006-01-02 15:04:05"))
			fmt.Printf("  inserted_ts: %s to %s\n",
				time.UnixMilli(minInsert).Format("2006-01-02 15:04:05"),
				time.UnixMilli(maxInsert).Format("2006-01-02 15:04:05"))
			fmt.Printf("  Current cutoff: %s\n", time.UnixMilli(cutoff).Format("2006-01-02 15:04:05"))
		}
		rows4.Close()
	}

	// Now check distance filtering
	rows2, err := db.Query(`SELECT LOWER(exchange), price, mark_price, notional_usd
		FROM liquidation_events
		WHERE symbol=? AND (event_ts>=? OR inserted_ts>=?)`, "ETHUSDT", cutoff, cutoff)
	if err != nil {
		log.Fatal(err)
	}
	defer rows2.Close()

	distanceStats := make(map[string]int)
	within07 := 0
	totalWithDistance := 0
	for rows2.Next() {
		var ex string
		var price, mark, notional float64
		if err := rows2.Scan(&ex, &price, &mark, &notional); err != nil {
			continue
		}
		if ex == "" || !(price > 0) || !(mark > 0) || !(notional > 0) {
			continue
		}

		distObs := (price - mark) / mark
		if distObs < 0 {
			distObs = -distObs
		}
		totalWithDistance++

		if distObs <= 0.7 {
			within07++
			distanceStats[ex] = distanceStats[ex] + 1
		}
	}

	fmt.Printf("\nDistance analysis:\n")
	fmt.Printf("Records with distance <= 0.7 (70%%): %d out of %d (%.1f%%)\n",
		within07, totalWithDistance, float64(within07)/float64(totalWithDistance)*100)
	for ex, count := range distanceStats {
		fmt.Printf("  %s: %d\n", ex, count)
	}
}
