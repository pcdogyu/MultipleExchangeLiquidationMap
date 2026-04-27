package liqmap

import (
	"strings"
	"time"
)

func (a *App) recordAnalysisDirectionSignal(snapshot AnalysisSnapshot) error {
	direction := strings.ToLower(strings.TrimSpace(snapshot.Overview.Direction))
	if direction != "up" && direction != "down" {
		return nil
	}

	symbol := strings.TrimSpace(snapshot.Symbol)
	if symbol == "" {
		symbol = defaultSymbol
	}

	generatedAt := snapshot.GeneratedAt
	if generatedAt <= 0 {
		generatedAt = time.Now().UnixMilli()
	}

	headline := strings.TrimSpace(snapshot.Broadcast.Headline)
	if headline == "" {
		headline = strings.TrimSpace(snapshot.Overview.Title)
	}

	summary := strings.TrimSpace(snapshot.Broadcast.Text)
	if summary == "" {
		summary = strings.TrimSpace(snapshot.Overview.Summary)
	}

	_, _ = a.db.Exec(`DELETE FROM analysis_direction_signals
		WHERE symbol=? AND source_group=? AND analysis_generated_at=?`,
		symbol, 5, generatedAt,
	)

	_, err := a.db.Exec(`INSERT INTO analysis_direction_signals(
			signal_ts, symbol, source_group, direction, signal_price,
			analysis_generated_at, headline, summary, verify_horizon_min
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		generatedAt,
		symbol,
		5,
		direction,
		snapshot.CurrentPrice,
		generatedAt,
		headline,
		summary,
		5,
	)
	return err
}
