package liqmap

func (a *App) recordAnalysisDirectionSignal(snapshot AnalysisSnapshot) error {
	record, ok := a.buildAnalysisDirectionSignalRecord(snapshot)
	if !ok {
		return nil
	}

	_, _ = a.db.Exec(`DELETE FROM analysis_direction_signals
		WHERE symbol=? AND source_group=? AND analysis_generated_at=?`,
		record.Symbol, record.SourceGroup, record.AnalysisGenerated,
	)

	_, err := a.db.Exec(`INSERT INTO analysis_direction_signals(
			signal_ts, symbol, source_group, direction, confidence, signal_price,
			analysis_generated_at, headline, summary, verify_horizon_min
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.SignalTS,
		record.Symbol,
		record.SourceGroup,
		record.Direction,
		record.Confidence,
		record.SignalPrice,
		record.AnalysisGenerated,
		record.Headline,
		record.Summary,
		record.VerifyHorizonMin,
	)
	return err
}
