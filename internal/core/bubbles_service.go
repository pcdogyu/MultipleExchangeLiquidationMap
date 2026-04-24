package liqmap

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) latestOKXClose() (float64, int64, error) {
	var resp struct {
		Code string     `json:"code"`
		Msg  string     `json:"msg"`
		Data [][]string `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/market/candles?instId=ETH-USDT-SWAP&bar=1m&limit=1", &resp); err != nil {
		return 0, 0, err
	}
	if resp.Code != "" && resp.Code != "0" {
		return 0, 0, fmt.Errorf("okx returned %s: %s", resp.Code, resp.Msg)
	}
	if len(resp.Data) == 0 || len(resp.Data[0]) < 5 {
		return 0, 0, errors.New("okx latest candle not found")
	}
	closePrice, err := strconv.ParseFloat(strings.TrimSpace(resp.Data[0][4]), 64)
	if err != nil || closePrice <= 0 {
		return 0, 0, errors.New("okx latest close invalid")
	}
	ts, _ := strconv.ParseInt(strings.TrimSpace(resp.Data[0][0]), 10, 64)
	return math.Round(closePrice*10) / 10, ts, nil
}

func (a *App) fetchKlines(interval string, limit int, startTS, endTS int64) (map[string]any, error) {
	interval = strings.ToLower(strings.TrimSpace(interval))
	if limit <= 0 {
		limit = 300
	}
	allowed := map[string]bool{"1m": true, "5m": true, "15m": true, "30m": true, "1h": true, "4h": true, "8h": true, "12h": true, "1d": true, "3d": true, "1w": true}
	binInterval := interval
	if interval == "" {
		binInterval = "15m"
	}
	if interval == "2m" {
		binInterval = "1m"
	}
	if interval == "10m" {
		binInterval = "5m"
	}
	if interval == "7d" {
		binInterval = "1w"
	}
	if !allowed[binInterval] {
		return nil, BadRequestError{Message: "unsupported interval"}
	}
	var rows [][]any
	url := fmt.Sprintf("https://api.binance.com/api/v3/klines?symbol=ETHUSDT&interval=%s&limit=%d", binInterval, limit)
	if startTS > 0 {
		url += fmt.Sprintf("&startTime=%d", startTS)
	}
	if endTS > 0 {
		url += fmt.Sprintf("&endTime=%d", endTS)
	}
	source := "binance:" + binInterval
	if err := a.fetchJSON(url, &rows); err != nil {
		okxRows, okxSource, okxErr := a.fetchOKXKlines(binInterval, limit, startTS, endTS)
		if okxErr != nil {
			return nil, fmt.Errorf("binance klines failed: %w; okx fallback failed: %v", err, okxErr)
		}
		rows = okxRows
		source = okxSource
	}
	return map[string]any{
		"interval": interval,
		"source":   source,
		"rows":     rows,
	}, nil
}

func (a *App) fetchOKXKlines(interval string, limit int, startTS, endTS int64) ([][]any, string, error) {
	bar := okxKlineBar(interval)
	if bar == "" {
		return nil, "", fmt.Errorf("unsupported okx interval %q", interval)
	}
	var resp struct {
		Code string     `json:"code"`
		Msg  string     `json:"msg"`
		Data [][]string `json:"data"`
	}
	url := fmt.Sprintf("https://www.okx.com/api/v5/market/candles?instId=ETH-USDT-SWAP&bar=%s&limit=%d", url.QueryEscape(bar), limit)
	if startTS > 0 {
		url += fmt.Sprintf("&after=%d", startTS)
	}
	if endTS > 0 {
		url += fmt.Sprintf("&before=%d", endTS)
	}
	if err := a.fetchJSON(url, &resp); err != nil {
		return nil, "", err
	}
	if resp.Code != "" && resp.Code != "0" {
		return nil, "", fmt.Errorf("okx returned %s: %s", resp.Code, resp.Msg)
	}
	rows := make([][]any, 0, len(resp.Data))
	for i := len(resp.Data) - 1; i >= 0; i-- {
		r := resp.Data[i]
		if len(r) < 5 {
			continue
		}
		rows = append(rows, []any{r[0], r[1], r[2], r[3], r[4]})
	}
	return rows, "okx:" + bar, nil
}

func okxKlineBar(interval string) string {
	switch interval {
	case "1m":
		return "1m"
	case "5m":
		return "5m"
	case "15m":
		return "15m"
	case "30m":
		return "30m"
	case "1h":
		return "1H"
	case "4h":
		return "4H"
	case "8h":
		return "6H"
	case "12h":
		return "12H"
	case "1d":
		return "1D"
	case "3d":
		return "3D"
	case "1w":
		return "1W"
	default:
		return ""
	}
}
