package liqmap

import "time"

func (a *App) BuildAnalysisSnapshot() (AnalysisSnapshot, error) {
	a.analysisMu.Lock()
	defer a.analysisMu.Unlock()
	if !a.analysisAt.IsZero() && time.Since(a.analysisAt) < 30*time.Second {
		return a.analysisCache, nil
	}
	snapshot, err := a.buildAnalysisSnapshot()
	if err != nil {
		return AnalysisSnapshot{}, err
	}
	a.analysisCache = snapshot
	a.analysisAt = time.Now()
	return snapshot, nil
}
