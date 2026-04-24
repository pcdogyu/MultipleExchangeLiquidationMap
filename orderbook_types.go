package liqmap

import "sync"

type Level struct {
	Price float64 `json:"price"`
	Qty   float64 `json:"qty"`
}

type OrderBook struct {
	mu            sync.RWMutex
	Exchange      string
	Symbol        string
	Bids          map[string]float64
	Asks          map[string]float64
	LastUpdateID  int64
	LastSeq       int64
	UpdatedTS     int64
	LastSnapshot  int64
	LastWSEventTS int64
}

type OrderBookHub struct {
	books map[string]*OrderBook
}
