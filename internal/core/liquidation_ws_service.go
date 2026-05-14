package liqmap

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	bybitLiquidationTopicCharLimit = 18000
	okxLiquidationBatchSize        = 80
	wsKeepaliveReadTimeout         = 75 * time.Second
	wsKeepalivePingInterval        = 20 * time.Second
	wsKeepaliveWriteTimeout        = 10 * time.Second
)

type okxLiquidationInstrument struct {
	InstID string
	CtVal  float64
}

func startWSKeepalive(ctx context.Context, conn *websocket.Conn, readTimeout, pingInterval time.Duration) func() {
	if readTimeout <= 0 {
		readTimeout = wsKeepaliveReadTimeout
	}
	if pingInterval <= 0 {
		pingInterval = wsKeepalivePingInterval
	}

	resetDeadline := func() {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	}
	resetDeadline()
	conn.SetPongHandler(func(string) error {
		resetDeadline()
		return nil
	})
	conn.SetPingHandler(func(appData string) error {
		resetDeadline()
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(wsKeepaliveWriteTimeout))
	})

	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, []byte("keepalive"), time.Now().Add(wsKeepaliveWriteTimeout)); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()

	return func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
	}
}

func normalizeLiquidationEventSymbol(exchange, symbol string) string {
	exchange = strings.ToLower(strings.TrimSpace(exchange))
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return defaultSymbol
	}
	switch exchange {
	case "okx":
		symbol = strings.TrimSuffix(symbol, "-SWAP")
		if idx := strings.Index(symbol, "_"); idx > 0 {
			symbol = symbol[:idx]
		}
		symbol = strings.ReplaceAll(symbol, "-", "")
	default:
		if idx := strings.Index(symbol, "_"); idx > 0 {
			symbol = symbol[:idx]
		}
		symbol = strings.ReplaceAll(symbol, "-", "")
	}
	if symbol == "" {
		return defaultSymbol
	}
	return symbol
}

func dedupeSortedStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	sort.Strings(items)
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != item {
			out = append(out, item)
		}
	}
	return out
}

func chunkStringsByCharLimit(items []string, maxChars int) [][]string {
	if maxChars <= 0 {
		maxChars = bybitLiquidationTopicCharLimit
	}
	out := make([][]string, 0, 8)
	cur := make([]string, 0, 256)
	curChars := 0
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		itemChars := len(item) + 4
		if len(cur) > 0 && curChars+itemChars > maxChars {
			out = append(out, cur)
			cur = make([]string, 0, 256)
			curChars = 0
		}
		cur = append(cur, item)
		curChars += itemChars
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func chunkOKXLiquidationInstruments(items []okxLiquidationInstrument, batchSize int) [][]okxLiquidationInstrument {
	if batchSize <= 0 {
		batchSize = okxLiquidationBatchSize
	}
	out := make([][]okxLiquidationInstrument, 0, (len(items)+batchSize-1)/batchSize)
	for start := 0; start < len(items); start += batchSize {
		end := start + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := make([]okxLiquidationInstrument, end-start)
		copy(batch, items[start:end])
		out = append(out, batch)
	}
	return out
}

func (a *App) ensureLiquidationWSState(exchange string) *liquidationWSState {
	exchange = strings.ToLower(strings.TrimSpace(exchange))
	a.liqWSMu.Lock()
	defer a.liqWSMu.Unlock()
	if a.liqWS == nil {
		a.liqWS = map[string]*liquidationWSState{}
	}
	state := a.liqWS[exchange]
	if state == nil {
		state = &liquidationWSState{Exchange: exchange}
		a.liqWS[exchange] = state
	}
	return state
}

func (a *App) setLiquidationWSSubscribed(exchange, mode string, subscribed int) {
	state := a.ensureLiquidationWSState(exchange)
	a.liqWSMu.Lock()
	defer a.liqWSMu.Unlock()
	state.Mode = strings.TrimSpace(mode)
	state.SubscribedTopics = subscribed
}

func (a *App) noteLiquidationWSConnected(exchange string) {
	state := a.ensureLiquidationWSState(exchange)
	a.liqWSMu.Lock()
	defer a.liqWSMu.Unlock()
	state.ActiveConns++
	state.LastConnectTS = time.Now().UnixMilli()
	state.LastError = ""
}

func (a *App) noteLiquidationWSDisconnected(exchange, errText string) {
	state := a.ensureLiquidationWSState(exchange)
	a.liqWSMu.Lock()
	defer a.liqWSMu.Unlock()
	if state.ActiveConns > 0 {
		state.ActiveConns--
	}
	state.LastDisconnectTS = time.Now().UnixMilli()
	if strings.TrimSpace(errText) != "" {
		state.LastError = strings.TrimSpace(errText)
	}
}

func (a *App) noteLiquidationWSError(exchange, errText string) {
	if strings.TrimSpace(errText) == "" {
		return
	}
	state := a.ensureLiquidationWSState(exchange)
	a.liqWSMu.Lock()
	defer a.liqWSMu.Unlock()
	state.LastError = strings.TrimSpace(errText)
}

func (a *App) noteLiquidationWSEvent(exchange string) {
	state := a.ensureLiquidationWSState(exchange)
	a.liqWSMu.Lock()
	defer a.liqWSMu.Unlock()
	state.LastEventTS = time.Now().UnixMilli()
}

func (a *App) fetchBybitLiquidationSymbols(category string) ([]string, error) {
	category = strings.ToLower(strings.TrimSpace(category))
	if category == "" {
		category = "linear"
	}
	cursor := ""
	symbols := make([]string, 0, 1024)
	for {
		rawURL := "https://api.bybit.com/v5/market/instruments-info?category=" + category + "&limit=1000"
		if cursor != "" {
			rawURL += "&cursor=" + url.QueryEscape(cursor)
		}
		var resp struct {
			RetCode int    `json:"retCode"`
			RetMsg  string `json:"retMsg"`
			Result  struct {
				NextPageCursor string `json:"nextPageCursor"`
				List           []struct {
					Symbol string `json:"symbol"`
					Status string `json:"status"`
				} `json:"list"`
			} `json:"result"`
		}
		if err := a.fetchJSON(rawURL, &resp); err != nil {
			return nil, err
		}
		if resp.RetCode != 0 {
			return nil, fmt.Errorf("bybit instruments retCode=%d msg=%s", resp.RetCode, resp.RetMsg)
		}
		for _, item := range resp.Result.List {
			if !strings.EqualFold(strings.TrimSpace(item.Status), "Trading") {
				continue
			}
			symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
			if symbol == "" {
				continue
			}
			symbols = append(symbols, symbol)
		}
		if strings.TrimSpace(resp.Result.NextPageCursor) == "" {
			break
		}
		cursor = strings.TrimSpace(resp.Result.NextPageCursor)
	}
	out := dedupeSortedStrings(symbols)
	a.mergeLiquidationSymbols(out)
	return out, nil
}

func (a *App) fetchOKXLiquidationInstruments() ([]okxLiquidationInstrument, error) {
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			InstID string `json:"instId"`
			State  string `json:"state"`
			CtVal  string `json:"ctVal"`
		} `json:"data"`
	}
	if err := a.fetchJSON("https://www.okx.com/api/v5/public/instruments?instType=SWAP", &resp); err != nil {
		return nil, err
	}
	out := make([]okxLiquidationInstrument, 0, len(resp.Data))
	for _, item := range resp.Data {
		if state := strings.ToLower(strings.TrimSpace(item.State)); state != "" && state != "live" {
			continue
		}
		instID := strings.ToUpper(strings.TrimSpace(item.InstID))
		if instID == "" {
			continue
		}
		ctVal := parseFloat(item.CtVal)
		if ctVal <= 0 {
			ctVal = 1
		}
		out = append(out, okxLiquidationInstrument{
			InstID: instID,
			CtVal:  ctVal,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].InstID < out[j].InstID
	})
	symbols := make([]string, 0, len(out))
	for _, item := range out {
		symbols = append(symbols, normalizeLiquidationEventSymbol("okx", item.InstID))
	}
	a.mergeLiquidationSymbols(symbols)
	return out, nil
}

func (a *App) runLiquidationBatchGroup(ctx context.Context, runners []func(context.Context) error) error {
	if len(runners) == 0 {
		return errors.New("no liquidation runners configured")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(runners))
	for _, runner := range runners {
		go func(run func(context.Context) error) {
			errCh <- run(runCtx)
		}(runner)
	}

	var runErr error
	for i := 0; i < len(runners); i++ {
		err := <-errCh
		if err == nil {
			continue
		}
		if runCtx.Err() != nil || errors.Is(err, context.Canceled) {
			continue
		}
		if runErr == nil {
			runErr = err
			cancel()
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return runErr
}

func (a *App) syncBinanceAllLiquidations(ctx context.Context) {
	const exchange = "binance"
	a.setLiquidationWSSubscribed(exchange, "all-market", -1)
	wsURL := "wss://fstream.binance.com/market/ws/!forceOrder@arr"
	for {
		if !a.waitExchangeRetry(ctx, exchange, 0) {
			return
		}
		err := a.runBinanceAllLiquidationsConn(ctx, wsURL)
		if err != nil && a.debug && ctx.Err() == nil {
			log.Printf("binance all-market liquidation ws err: %v", err)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			a.noteLiquidationWSError(exchange, err.Error())
		}
		if !a.waitExchangeRetry(ctx, exchange, 2*time.Second) {
			return
		}
	}
}

func (a *App) runBinanceAllLiquidationsConn(ctx context.Context, wsURL string) (err error) {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	stopKeepalive := startWSKeepalive(ctx, conn, wsKeepaliveReadTimeout, wsKeepalivePingInterval)
	defer stopKeepalive()

	a.noteLiquidationWSConnected("binance")
	defer func() {
		errText := ""
		if err != nil && ctx.Err() == nil {
			errText = err.Error()
		}
		a.noteLiquidationWSDisconnected("binance", errText)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		var raw any
		if err = conn.ReadJSON(&raw); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsKeepaliveReadTimeout))
		msgs := []map[string]any{}
		switch payload := raw.(type) {
		case map[string]any:
			msgs = append(msgs, payload)
		case []any:
			for _, item := range payload {
				row, ok := item.(map[string]any)
				if ok {
					msgs = append(msgs, row)
				}
			}
		}
		for _, msg := range msgs {
			o, ok := msg["o"].(map[string]any)
			if !ok {
				continue
			}
			symbol := fmt.Sprint(o["s"])
			if strings.Contains(symbol, "_") && strings.TrimSpace(fmt.Sprint(o["ps"])) != "" {
				symbol = fmt.Sprint(o["ps"])
			}
			side := fmt.Sprint(o["S"])
			price := parseAnyFloat(o["ap"])
			if price <= 0 {
				price = parseAnyFloat(o["p"])
			}
			qty := parseAnyFloat(o["q"])
			if qty <= 0 {
				qty = parseAnyFloat(o["z"])
			}
			ts := int64(parseAnyFloat(o["T"]))
			a.insertLiquidationEvent("binance", symbol, side, side, price, qty, 0, ts)
			a.noteLiquidationWSEvent("binance")
		}
	}
}

func (a *App) syncBybitAllLiquidations(ctx context.Context) {
	const exchange = "bybit"
	for {
		if !a.waitExchangeRetry(ctx, exchange, 0) {
			return
		}

		linearSymbols, err := a.fetchBybitLiquidationSymbols("linear")
		if err != nil {
			a.noteLiquidationWSError(exchange, err.Error())
			if a.debug && !isExchangePausedError(err) {
				log.Printf("bybit linear liquidation symbols err: %v", err)
			}
			if !a.waitExchangeRetry(ctx, exchange, 5*time.Second) {
				return
			}
			continue
		}
		inverseSymbols, err := a.fetchBybitLiquidationSymbols("inverse")
		if err != nil {
			a.noteLiquidationWSError(exchange, err.Error())
			if a.debug && !isExchangePausedError(err) {
				log.Printf("bybit inverse liquidation symbols err: %v", err)
			}
			if !a.waitExchangeRetry(ctx, exchange, 5*time.Second) {
				return
			}
			continue
		}

		linearTopics := make([]string, 0, len(linearSymbols))
		for _, symbol := range linearSymbols {
			linearTopics = append(linearTopics, "allLiquidation."+symbol)
		}
		inverseTopics := make([]string, 0, len(inverseSymbols))
		for _, symbol := range inverseSymbols {
			inverseTopics = append(inverseTopics, "allLiquidation."+symbol)
		}
		batchesLinear := chunkStringsByCharLimit(linearTopics, bybitLiquidationTopicCharLimit)
		batchesInverse := chunkStringsByCharLimit(inverseTopics, bybitLiquidationTopicCharLimit)

		runners := make([]func(context.Context) error, 0, len(batchesLinear)+len(batchesInverse))
		for _, batch := range batchesLinear {
			topics := append([]string(nil), batch...)
			runners = append(runners, func(runCtx context.Context) error {
				return a.runBybitLiquidationBatch(runCtx, "linear", topics)
			})
		}
		for _, batch := range batchesInverse {
			topics := append([]string(nil), batch...)
			runners = append(runners, func(runCtx context.Context) error {
				return a.runBybitLiquidationBatch(runCtx, "inverse", topics)
			})
		}

		a.setLiquidationWSSubscribed(exchange, "batched-public", len(linearTopics)+len(inverseTopics))
		if len(runners) == 0 {
			a.noteLiquidationWSError(exchange, "no Bybit liquidation topics available")
			if !a.waitExchangeRetry(ctx, exchange, 30*time.Second) {
				return
			}
			continue
		}

		err = a.runLiquidationBatchGroup(ctx, runners)
		if err != nil {
			a.noteLiquidationWSError(exchange, err.Error())
			if a.debug {
				log.Printf("bybit batched liquidation ws err: %v", err)
			}
		}
		if !a.waitExchangeRetry(ctx, exchange, 2*time.Second) {
			return
		}
	}
}

func (a *App) runBybitLiquidationBatch(ctx context.Context, category string, topics []string) (err error) {
	wsURL := "wss://stream.bybit.com/v5/public/" + strings.ToLower(strings.TrimSpace(category))
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	stopKeepalive := startWSKeepalive(ctx, conn, wsKeepaliveReadTimeout, wsKeepalivePingInterval)
	defer stopKeepalive()

	sub := map[string]any{"op": "subscribe", "args": topics}
	if err = conn.WriteJSON(sub); err != nil {
		return err
	}

	a.noteLiquidationWSConnected("bybit")
	defer func() {
		errText := ""
		if err != nil && ctx.Err() == nil {
			errText = err.Error()
		}
		a.noteLiquidationWSDisconnected("bybit", errText)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		var msg map[string]any
		if err = conn.ReadJSON(&msg); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsKeepaliveReadTimeout))
		dataArr, ok := msg["data"].([]any)
		if !ok {
			continue
		}
		for _, item := range dataArr {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			symbol := fmt.Sprint(row["s"])
			side := invertLiquidationSide(fmt.Sprint(row["S"]))
			price := parseAnyFloat(row["p"])
			qty := parseAnyFloat(row["v"])
			if qty <= 0 {
				qty = parseAnyFloat(row["q"])
			}
			ts := int64(parseAnyFloat(row["T"]))
			a.insertLiquidationEvent("bybit", symbol, side, side, price, qty, 0, ts)
			a.noteLiquidationWSEvent("bybit")
		}
	}
}

func (a *App) syncOKXAllLiquidations(ctx context.Context) {
	const exchange = "okx"
	for {
		if !a.waitExchangeRetry(ctx, exchange, 0) {
			return
		}
		instruments, err := a.fetchOKXLiquidationInstruments()
		if err != nil {
			a.noteLiquidationWSError(exchange, err.Error())
			if a.debug && !isExchangePausedError(err) {
				log.Printf("okx liquidation instruments err: %v", err)
			}
			if !a.waitExchangeRetry(ctx, exchange, 5*time.Second) {
				return
			}
			continue
		}

		batches := chunkOKXLiquidationInstruments(instruments, okxLiquidationBatchSize)
		runners := make([]func(context.Context) error, 0, len(batches))
		for _, batch := range batches {
			insts := append([]okxLiquidationInstrument(nil), batch...)
			runners = append(runners, func(runCtx context.Context) error {
				return a.runOKXLiquidationBatch(runCtx, insts)
			})
		}

		a.setLiquidationWSSubscribed(exchange, "batched-public", len(instruments))
		if len(runners) == 0 {
			a.noteLiquidationWSError(exchange, "no OKX swap liquidation instruments available")
			if !a.waitExchangeRetry(ctx, exchange, 30*time.Second) {
				return
			}
			continue
		}

		err = a.runLiquidationBatchGroup(ctx, runners)
		if err != nil {
			a.noteLiquidationWSError(exchange, err.Error())
			if a.debug {
				log.Printf("okx batched liquidation ws err: %v", err)
			}
		}
		if !a.waitExchangeRetry(ctx, exchange, 2*time.Second) {
			return
		}
	}
}

func (a *App) runOKXLiquidationBatch(ctx context.Context, instruments []okxLiquidationInstrument) (err error) {
	conn, _, err := websocket.DefaultDialer.Dial("wss://ws.okx.com:8443/ws/v5/public", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	stopKeepalive := startWSKeepalive(ctx, conn, wsKeepaliveReadTimeout, wsKeepalivePingInterval)
	defer stopKeepalive()

	args := make([]map[string]string, 0, len(instruments))
	ctValByInst := make(map[string]float64, len(instruments))
	for _, inst := range instruments {
		args = append(args, map[string]string{
			"channel":  "liquidation-orders",
			"instType": "SWAP",
			"instId":   inst.InstID,
		})
		ctValByInst[inst.InstID] = inst.CtVal
	}
	sub := map[string]any{"op": "subscribe", "args": args}
	if err = conn.WriteJSON(sub); err != nil {
		return err
	}

	a.noteLiquidationWSConnected("okx")
	defer func() {
		errText := ""
		if err != nil && ctx.Err() == nil {
			errText = err.Error()
		}
		a.noteLiquidationWSDisconnected("okx", errText)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		var msg map[string]any
		if err = conn.ReadJSON(&msg); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsKeepaliveReadTimeout))
		dataArr, ok := msg["data"].([]any)
		if !ok {
			continue
		}
		for _, item := range dataArr {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			instID := strings.ToUpper(strings.TrimSpace(fmt.Sprint(row["instId"])))
			side := fmt.Sprint(row["side"])
			posSide := fmt.Sprint(row["posSide"])
			if strings.TrimSpace(posSide) == "" {
				posSide = side
			}
			price := parseAnyFloat(row["bkPx"])
			if price <= 0 {
				price = parseAnyFloat(row["px"])
			}
			ctVal := ctValByInst[instID]
			if ctVal <= 0 {
				ctVal = 1
			}
			qty := parseAnyFloat(row["sz"]) * ctVal
			ts := int64(parseAnyFloat(row["ts"]))
			a.insertLiquidationEvent("okx", instID, posSide, side, price, qty, 0, ts)
			a.noteLiquidationWSEvent("okx")
		}
	}
}
