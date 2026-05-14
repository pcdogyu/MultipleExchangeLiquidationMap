package liqmap

import (
	"strings"
	"time"
)

func (a *App) listPriceWallEvents(page, limit, minutes int, side, mode string) (any, error) {
	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = 25
	}
	if minutes <= 0 {
		minutes = 30
	}
	if side != "" && side != "bid" && side != "ask" {
		return nil, BadRequestError{Message: "invalid side"}
	}
	if mode != "" && mode != "weighted" && mode != "merged" {
		return nil, BadRequestError{Message: "invalid mode"}
	}
	if page == 1 && limit == 25 && minutes == 30 && side == "" && mode == "" {
		cutoff := time.Now().Add(-30 * time.Minute).UnixMilli()
		rows, err := a.db.Query(`SELECT side, price, peak_notional_usd, duration_ms, event_ts, mode
			FROM price_wall_events
			WHERE event_ts>=?
			ORDER BY event_ts DESC
			LIMIT 200`, cutoff)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := make([]PriceWallEvent, 0, 200)
		for rows.Next() {
			var e PriceWallEvent
			if err := rows.Scan(&e.Side, &e.Price, &e.Peak, &e.DurationMS, &e.EventTS, &e.Mode); err != nil {
				continue
			}
			out = append(out, e)
		}
		return out, nil
	}

	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).UnixMilli()
	baseSQL := ` FROM price_wall_events WHERE event_ts>=?`
	args := []any{cutoff}
	if side != "" {
		baseSQL += ` AND side=?`
		args = append(args, side)
	}
	if mode != "" {
		baseSQL += ` AND mode=?`
		args = append(args, mode)
	}

	var total int
	if err := a.db.QueryRow(`SELECT COUNT(1)`+baseSQL, args...).Scan(&total); err != nil {
		return nil, err
	}
	offset := (page - 1) * limit
	rows, err := a.db.Query(`SELECT side, price, peak_notional_usd, duration_ms, event_ts, mode`+baseSQL+` ORDER BY event_ts DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PriceWallEvent, 0, limit)
	for rows.Next() {
		var e PriceWallEvent
		if err := rows.Scan(&e.Side, &e.Price, &e.Peak, &e.DurationMS, &e.EventTS, &e.Mode); err != nil {
			continue
		}
		out = append(out, e)
	}
	return map[string]any{
		"page":      page,
		"page_size": limit,
		"total":     total,
		"minutes":   minutes,
		"side":      side,
		"mode":      mode,
		"rows":      out,
	}, nil
}

func (a *App) recordPriceWallEvent(req PriceWallEvent) error {
	req.Side = strings.ToLower(strings.TrimSpace(req.Side))
	if req.Side != "bid" && req.Side != "ask" {
		return BadRequestError{Message: "invalid side"}
	}
	if req.Price <= 0 || req.Peak <= 0 || req.DurationMS <= 0 {
		return BadRequestError{Message: "invalid payload"}
	}
	req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
	if req.Mode != "weighted" && req.Mode != "merged" {
		req.Mode = "weighted"
	}
	if req.EventTS <= 0 {
		req.EventTS = time.Now().UnixMilli()
	}
	_, err := a.db.Exec(`INSERT INTO price_wall_events(side, price, peak_notional_usd, duration_ms, event_ts, mode, inserted_ts)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		req.Side, req.Price, req.Peak, req.DurationMS, req.EventTS, req.Mode, time.Now().UnixMilli())
	return err
}
