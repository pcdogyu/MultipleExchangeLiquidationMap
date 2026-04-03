#!/usr/bin/env python3
# -*- coding: utf-8 -*-

"""
ETH liquidation map estimator (single-file + SQLite)

功能：
1. 采集 Binance / Bybit 的 ETHUSDT 实时清算、标记价格、OI
2. 落库 SQLite
3. 计算：
   - 已发生清算统计（1m / 5m / 15m）
   - 预估清算热区（10/20/30/...点内）
   - 最长柱（按 price bucket 聚合后的最大桶）
4. 控制台输出表格
5. 周期性把 band report 存入 SQLite

安装：
    pip install aiohttp websockets

运行：
    python liqmap_single.py

可选环境变量：
    SYMBOL=ETHUSDT
    DB_PATH=liqmap.db
    REPORT_INTERVAL=5
    RETENTION_MINUTES=240
"""

import os
import sys
import json
import math
import time
import signal
import sqlite3
import asyncio
from dataclasses import dataclass
from typing import Optional, Literal, Dict, List, Tuple
from collections import defaultdict

import aiohttp
import websockets

# =========================
# Config
# =========================

SYMBOL = os.getenv("SYMBOL", "ETHUSDT").upper()
DB_PATH = os.getenv("DB_PATH", "liqmap.db")
REPORT_INTERVAL = int(os.getenv("REPORT_INTERVAL", "5"))
RETENTION_MINUTES = int(os.getenv("RETENTION_MINUTES", "240"))  # DB event retention
BANDS = [10, 20, 30, 40, 50, 60, 80, 100, 150]
LONGEST_BAR_BUCKET = float(os.getenv("LONGEST_BAR_BUCKET", "5"))  # 单个价格桶宽度
PRINT_TOP_N_EVENTS = int(os.getenv("PRINT_TOP_N_EVENTS", "8"))

BINANCE_WS_MARK = f"wss://fstream.binance.com/ws/{SYMBOL.lower()}@markPrice@1s"
BINANCE_WS_FORCE = "wss://fstream.binance.com/ws/!forceOrder@arr"
BINANCE_REST = "https://fapi.binance.com"
BYBIT_WS = "wss://stream.bybit.com/v5/public/linear"

LEVERAGE_BUCKETS = [
    (5, 0.20),
    (10, 0.35),
    (20, 0.30),
    (50, 0.15),
]

ENTRY_OFFSETS = [-0.03, -0.02, -0.01, 0.00, 0.01, 0.02, 0.03]
ENTRY_OFFSET_WEIGHTS = [0.08, 0.12, 0.18, 0.24, 0.18, 0.12, 0.08]

# =========================
# Helpers
# =========================

def now_ms() -> int:
    return int(time.time() * 1000)

def safe_float(v, default=0.0) -> float:
    try:
        if v is None:
            return default
        if isinstance(v, str) and v.strip() == "":
            return default
        return float(v)
    except Exception:
        return default

def fmt_usd(v: float) -> str:
    if v is None:
        return "-"
    if abs(v) >= 1e8:
        return f"{v/1e8:.2f}亿"
    if abs(v) >= 1e6:
        return f"{v/1e6:.2f}M"
    if abs(v) >= 1e3:
        return f"{v/1e3:.2f}K"
    return f"{v:.2f}"

def fmt_price(v: float) -> str:
    if v is None:
        return "-"
    return f"{v:.1f}"

def utc_ts_str(ms: int) -> str:
    return time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(ms / 1000))

# =========================
# Data classes
# =========================

Side = Literal["long", "short"]

@dataclass
class LiquidationEvent:
    exchange: str
    symbol: str
    side: Side              # 被清算的是 long or short
    raw_side: str           # 原始 side
    qty: float
    price: float            # 清算/破产价格/成交参考价
    mark_price: float
    notional_usd: float
    event_ts: int

@dataclass
class MarketState:
    exchange: str
    symbol: str
    mark_price: float
    oi_qty: Optional[float] = None
    oi_value_usd: Optional[float] = None
    funding_rate: Optional[float] = None
    updated_ts: int = 0

# =========================
# SQLite store
# =========================

class SQLiteStore:
    def __init__(self, db_path: str):
        self.db_path = db_path
        self.conn = sqlite3.connect(db_path, check_same_thread=False)
        self.conn.row_factory = sqlite3.Row
        self._init_db()

    def _init_db(self):
        cur = self.conn.cursor()
        cur.execute("PRAGMA journal_mode=WAL;")
        cur.execute("PRAGMA synchronous=NORMAL;")
        cur.execute("""
        CREATE TABLE IF NOT EXISTS market_state (
            exchange TEXT NOT NULL,
            symbol TEXT NOT NULL,
            mark_price REAL,
            oi_qty REAL,
            oi_value_usd REAL,
            funding_rate REAL,
            updated_ts INTEGER NOT NULL,
            PRIMARY KEY (exchange, symbol)
        );
        """)
        cur.execute("""
        CREATE TABLE IF NOT EXISTS liquidation_events (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            exchange TEXT NOT NULL,
            symbol TEXT NOT NULL,
            side TEXT NOT NULL,
            raw_side TEXT,
            qty REAL NOT NULL,
            price REAL NOT NULL,
            mark_price REAL NOT NULL,
            notional_usd REAL NOT NULL,
            event_ts INTEGER NOT NULL,
            inserted_ts INTEGER NOT NULL
        );
        """)
        cur.execute("CREATE INDEX IF NOT EXISTS idx_liq_symbol_ts ON liquidation_events(symbol, event_ts);")
        cur.execute("CREATE INDEX IF NOT EXISTS idx_liq_exchange_ts ON liquidation_events(exchange, event_ts);")

        cur.execute("""
        CREATE TABLE IF NOT EXISTS band_reports (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            report_ts INTEGER NOT NULL,
            symbol TEXT NOT NULL,
            current_price REAL NOT NULL,
            band INTEGER NOT NULL,
            up_price REAL NOT NULL,
            up_notional_usd REAL NOT NULL,
            down_price REAL NOT NULL,
            down_notional_usd REAL NOT NULL
        );
        """)
        cur.execute("CREATE INDEX IF NOT EXISTS idx_band_symbol_ts ON band_reports(symbol, report_ts);")

        cur.execute("""
        CREATE TABLE IF NOT EXISTS longest_bar_reports (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            report_ts INTEGER NOT NULL,
            symbol TEXT NOT NULL,
            side TEXT NOT NULL,
            bucket_size REAL NOT NULL,
            bucket_price REAL NOT NULL,
            bucket_notional_usd REAL NOT NULL
        );
        """)
        cur.execute("CREATE INDEX IF NOT EXISTS idx_longest_symbol_ts ON longest_bar_reports(symbol, report_ts);")

        self.conn.commit()

    def upsert_market_state(self, state: MarketState):
        self.conn.execute("""
        INSERT INTO market_state(exchange, symbol, mark_price, oi_qty, oi_value_usd, funding_rate, updated_ts)
        VALUES(?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(exchange, symbol) DO UPDATE SET
            mark_price=excluded.mark_price,
            oi_qty=COALESCE(excluded.oi_qty, market_state.oi_qty),
            oi_value_usd=COALESCE(excluded.oi_value_usd, market_state.oi_value_usd),
            funding_rate=COALESCE(excluded.funding_rate, market_state.funding_rate),
            updated_ts=excluded.updated_ts;
        """, (
            state.exchange, state.symbol, state.mark_price,
            state.oi_qty, state.oi_value_usd, state.funding_rate, state.updated_ts
        ))
        self.conn.commit()

    def insert_liquidation_event(self, event: LiquidationEvent):
        self.conn.execute("""
        INSERT INTO liquidation_events(
            exchange, symbol, side, raw_side, qty, price, mark_price, notional_usd, event_ts, inserted_ts
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """, (
            event.exchange, event.symbol, event.side, event.raw_side, event.qty,
            event.price, event.mark_price, event.notional_usd, event.event_ts, now_ms()
        ))
        self.conn.commit()

    def get_market_state(self, exchange: str, symbol: str) -> Optional[MarketState]:
        row = self.conn.execute("""
        SELECT * FROM market_state WHERE exchange=? AND symbol=?
        """, (exchange, symbol)).fetchone()
        if not row:
            return None
        return MarketState(
            exchange=row["exchange"],
            symbol=row["symbol"],
            mark_price=row["mark_price"] or 0.0,
            oi_qty=row["oi_qty"],
            oi_value_usd=row["oi_value_usd"],
            funding_rate=row["funding_rate"],
            updated_ts=row["updated_ts"],
        )

    def list_market_states(self, symbol: str) -> List[MarketState]:
        rows = self.conn.execute("""
        SELECT * FROM market_state WHERE symbol=? ORDER BY exchange
        """, (symbol,)).fetchall()
        out = []
        for row in rows:
            out.append(MarketState(
                exchange=row["exchange"],
                symbol=row["symbol"],
                mark_price=row["mark_price"] or 0.0,
                oi_qty=row["oi_qty"],
                oi_value_usd=row["oi_value_usd"],
                funding_rate=row["funding_rate"],
                updated_ts=row["updated_ts"],
            ))
        return out

    def get_recent_events(self, symbol: str, seconds: int) -> List[LiquidationEvent]:
        cutoff = now_ms() - seconds * 1000
        rows = self.conn.execute("""
        SELECT * FROM liquidation_events
        WHERE symbol=? AND event_ts>=?
        ORDER BY event_ts DESC
        """, (symbol, cutoff)).fetchall()
        out = []
        for row in rows:
            out.append(LiquidationEvent(
                exchange=row["exchange"],
                symbol=row["symbol"],
                side=row["side"],
                raw_side=row["raw_side"] or "",
                qty=row["qty"],
                price=row["price"],
                mark_price=row["mark_price"],
                notional_usd=row["notional_usd"],
                event_ts=row["event_ts"],
            ))
        return out

    def insert_band_report(self, report_ts: int, symbol: str, current_price: float, rows: List[dict]):
        self.conn.executemany("""
        INSERT INTO band_reports(
            report_ts, symbol, current_price, band, up_price, up_notional_usd, down_price, down_notional_usd
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        """, [
            (
                report_ts, symbol, current_price, row["band"],
                row["up_price"], row["up_notional_usd"],
                row["down_price"], row["down_notional_usd"]
            ) for row in rows
        ])
        self.conn.commit()

    def insert_longest_bar_report(self, report_ts: int, symbol: str, side: str, bucket_size: float, bucket_price: float, bucket_notional_usd: float):
        self.conn.execute("""
        INSERT INTO longest_bar_reports(report_ts, symbol, side, bucket_size, bucket_price, bucket_notional_usd)
        VALUES (?, ?, ?, ?, ?, ?)
        """, (report_ts, symbol, side, bucket_size, bucket_price, bucket_notional_usd))
        self.conn.commit()

    def cleanup_old_events(self, retention_minutes: int):
        cutoff = now_ms() - retention_minutes * 60 * 1000
        self.conn.execute("DELETE FROM liquidation_events WHERE event_ts < ?", (cutoff,))
        self.conn.commit()

# =========================
# Modeling
# =========================

def infer_long_short_weights(funding_rate: Optional[float]) -> Tuple[float, float]:
    # 粗略偏置：funding > 0 视为偏多，funding < 0 视为偏空
    if funding_rate is None:
        return 0.5, 0.5
    bias = max(min(funding_rate * 500.0, 0.10), -0.10)
    long_w = 0.5 + bias
    short_w = 1.0 - long_w
    return long_w, short_w

def approx_liq_price(entry: float, leverage: float, side: str, mmr: float = 0.005) -> float:
    # 工程近似，不是交易所精确清算公式
    if side == "long":
        return entry * (1.0 - 1.0 / leverage + mmr)
    return entry * (1.0 + 1.0 / leverage - mmr)

def build_estimated_distribution(mark_price: float, oi_value_usd: float, funding_rate: Optional[float]) -> List[dict]:
    if mark_price <= 0 or oi_value_usd <= 0:
        return []

    long_w, short_w = infer_long_short_weights(funding_rate)
    dist = []

    for side, side_w in [("long", long_w), ("short", short_w)]:
        for lev, lev_w in LEVERAGE_BUCKETS:
            for off, off_w in zip(ENTRY_OFFSETS, ENTRY_OFFSET_WEIGHTS):
                alloc = oi_value_usd * side_w * lev_w * off_w
                entry = mark_price * (1.0 + off)
                liq = approx_liq_price(entry, lev, side)
                dist.append({
                    "side": side,
                    "entry_price": entry,
                    "liq_price": liq,
                    "notional_usd": alloc,
                })
    return dist

def merged_distribution(states: List[MarketState]) -> List[dict]:
    dist = []
    for s in states:
        if s.mark_price <= 0:
            continue
        oi_value = s.oi_value_usd
        if (oi_value is None or oi_value <= 0) and s.oi_qty:
            oi_value = s.oi_qty * s.mark_price
        if oi_value is None or oi_value <= 0:
            continue
        dist.extend(build_estimated_distribution(
            mark_price=s.mark_price,
            oi_value_usd=oi_value,
            funding_rate=s.funding_rate,
        ))
    return dist

def weighted_current_price(states: List[MarketState]) -> Optional[float]:
    rows = []
    for s in states:
        if s.mark_price <= 0:
            continue
        w = s.oi_value_usd or ((s.oi_qty or 0.0) * s.mark_price)
        if not w or w <= 0:
            w = 1.0
        rows.append((s.mark_price, w))
    if not rows:
        return None
    total_w = sum(w for _, w in rows)
    return sum(p * w for p, w in rows) / total_w

def cumulative_bands(distribution: List[dict], current_price: float, bands: List[int]) -> Tuple[Dict[int, float], Dict[int, float]]:
    up = {}
    down = {}
    for b in bands:
        up_price = current_price + b
        down_price = current_price - b

        up[b] = sum(
            x["notional_usd"] for x in distribution
            if x["side"] == "short" and current_price < x["liq_price"] <= up_price
        )
        down[b] = sum(
            x["notional_usd"] for x in distribution
            if x["side"] == "long" and down_price <= x["liq_price"] < current_price
        )
    return up, down

def longest_bar(distribution: List[dict], side: str, bucket_size: float = 5.0) -> Tuple[Optional[float], float]:
    """
    side='short' -> 上方空单清算最长柱
    side='long'  -> 下方多单清算最长柱
    """
    bins = defaultdict(float)
    for x in distribution:
        if x["side"] != side:
            continue
        liq_price = x["liq_price"]
        bucket = round(math.floor(liq_price / bucket_size) * bucket_size, 8)
        bins[bucket] += x["notional_usd"]

    if not bins:
        return None, 0.0
    best_price, best_notional = max(bins.items(), key=lambda kv: kv[1])
    return best_price, best_notional

def build_band_rows(current_price: float, up: Dict[int, float], down: Dict[int, float], bands: List[int]) -> List[dict]:
    rows = []
    for b in bands:
        rows.append({
            "band": b,
            "up_price": round(current_price + b, 1),
            "up_notional_usd": up.get(b, 0.0),
            "down_price": round(current_price - b, 1),
            "down_notional_usd": down.get(b, 0.0),
        })
    return rows

def realized_summary(events: List[LiquidationEvent]) -> dict:
    agg = defaultdict(float)
    for e in events:
        agg[(e.exchange, e.side)] += e.notional_usd

    rows = []
    for (exchange, side), value in sorted(agg.items()):
        rows.append({
            "exchange": exchange,
            "side": side,
            "notional_usd": value,
        })
    return {"rows": rows}

# =========================
# Pretty print
# =========================

def print_divider(title: str = ""):
    line = "=" * 96
    if title:
        print(f"\n{line}\n{title}\n{line}")
    else:
        print(f"\n{line}")

def print_market_states(states: List[MarketState]):
    print_divider("市场状态")
    print(f"{'交易所':<10} {'标记价':>12} {'OI数量':>16} {'OI价值USD':>16} {'Funding':>12} {'更新时间':>20}")
    for s in states:
        print(
            f"{s.exchange:<10} "
            f"{fmt_price(s.mark_price):>12} "
            f"{(f'{s.oi_qty:.4f}' if s.oi_qty is not None else '-'):>16} "
            f"{fmt_usd(s.oi_value_usd) if s.oi_value_usd is not None else '-':>16} "
            f"{(f'{s.funding_rate:.6f}' if s.funding_rate is not None else '-'):>12} "
            f"{utc_ts_str(s.updated_ts):>20}"
        )

def print_realized_block(title: str, events: List[LiquidationEvent]):
    print_divider(title)
    summary = realized_summary(events)
    if not summary["rows"]:
        print("暂无数据")
        return

    print(f"{'交易所':<10} {'被清算方向':<10} {'名义价值':>16}")
    for row in summary["rows"]:
        print(f"{row['exchange']:<10} {row['side']:<10} {fmt_usd(row['notional_usd']):>16}")

    print("\n最近大额事件：")
    print(f"{'时间':<20} {'交易所':<10} {'方向':<8} {'价格':>12} {'数量':>14} {'名义价值':>16}")
    for e in sorted(events, key=lambda x: x.notional_usd, reverse=True)[:PRINT_TOP_N_EVENTS]:
        print(
            f"{utc_ts_str(e.event_ts):<20} {e.exchange:<10} {e.side:<8} "
            f"{fmt_price(e.price):>12} {e.qty:>14.4f} {fmt_usd(e.notional_usd):>16}"
        )

def print_band_table(current_price: float, rows: List[dict], longest_short: Tuple[Optional[float], float], longest_long: Tuple[Optional[float], float]):
    print_divider(f"{SYMBOL} 清算热区速报  当前价={fmt_price(current_price)}")
    print(f"{'点数阈值':<10} {'上方空单价':>12} {'上方空单规模':>18} {'下方多单价':>12} {'下方多单规模':>18}")
    for row in rows:
        print(
            f"{str(row['band'])+'点内':<10} "
            f"{fmt_price(row['up_price']):>12} "
            f"{fmt_usd(row['up_notional_usd']):>18} "
            f"{fmt_price(row['down_price']):>12} "
            f"{fmt_usd(row['down_notional_usd']):>18}"
        )

    short_price, short_notional = longest_short
    long_price, long_notional = longest_long
    print("\n最长柱：")
    print(f"  上方空单: 价格={fmt_price(short_price) if short_price is not None else '-'}  规模={fmt_usd(short_notional)}")
    print(f"  下方多单: 价格={fmt_price(long_price) if long_price is not None else '-'}  规模={fmt_usd(long_notional)}")

# =========================
# Normalizers
# =========================

def normalize_binance_force_order(msg: dict, mark_price: float) -> LiquidationEvent:
    """
    Binance forceOrder 文档只提供订单 side，没有直接给“被清算仓位方向”。
    这里按平仓方向做工程约定：
      SELL -> 视为 long liquidation
      BUY  -> 视为 short liquidation
    """
    o = msg["o"]
    raw_side = str(o.get("S", "")).upper()
    side: Side = "long" if raw_side == "SELL" else "short"
    qty = safe_float(o.get("q"))
    price = safe_float(o.get("ap")) or safe_float(o.get("p"))
    return LiquidationEvent(
        exchange="binance",
        symbol=o["s"],
        side=side,
        raw_side=raw_side,
        qty=qty,
        price=price,
        mark_price=mark_price,
        notional_usd=qty * price,
        event_ts=int(o["T"]),
    )

def normalize_bybit_all_liq(msg: dict, mark_price: float) -> List[LiquidationEvent]:
    data = msg.get("data", [])
    rows = data if isinstance(data, list) else [data]
    out = []

    for row in rows:
        raw_side = str(row.get("S", ""))
        # Bybit 文档：Buy 表示 long position has been liquidated
        side: Side = "long" if raw_side == "Buy" else "short"
        qty = safe_float(row.get("v"))
        price = safe_float(row.get("p"))
        out.append(LiquidationEvent(
            exchange="bybit",
            symbol=row["s"],
            side=side,
            raw_side=raw_side,
            qty=qty,
            price=price,
            mark_price=mark_price,
            notional_usd=qty * price,
            event_ts=int(row["T"]),
        ))
    return out

# =========================
# Async tasks
# =========================

async def run_binance_mark_ws(store: SQLiteStore, stop_event: asyncio.Event):
    while not stop_event.is_set():
        try:
            async with websockets.connect(BINANCE_WS_MARK, ping_interval=20, ping_timeout=20) as ws:
                print("[binance-mark] connected")
                async for raw in ws:
                    if stop_event.is_set():
                        break
                    msg = json.loads(raw)
                    symbol = msg.get("s")
                    if symbol != SYMBOL:
                        continue
                    state = MarketState(
                        exchange="binance",
                        symbol=symbol,
                        mark_price=safe_float(msg.get("p")),
                        funding_rate=safe_float(msg.get("r"), None),
                        updated_ts=int(msg.get("E", now_ms())),
                    )
                    store.upsert_market_state(state)
        except Exception as e:
            print(f"[binance-mark] reconnect after error: {e}")
            await asyncio.sleep(2)

async def run_binance_force_ws(store: SQLiteStore, stop_event: asyncio.Event):
    while not stop_event.is_set():
        try:
            async with websockets.connect(BINANCE_WS_FORCE, ping_interval=20, ping_timeout=20) as ws:
                print("[binance-force] connected")
                async for raw in ws:
                    if stop_event.is_set():
                        break
                    msg = json.loads(raw)
                    if msg.get("e") != "forceOrder":
                        continue
                    if msg.get("o", {}).get("s") != SYMBOL:
                        continue

                    s = store.get_market_state("binance", SYMBOL)
                    mark_price = s.mark_price if s else 0.0
                    event = normalize_binance_force_order(msg, mark_price)
                    store.insert_liquidation_event(event)
        except Exception as e:
            print(f"[binance-force] reconnect after error: {e}")
            await asyncio.sleep(2)

async def poll_binance_open_interest(store: SQLiteStore, stop_event: asyncio.Event, interval: int = 10):
    async with aiohttp.ClientSession() as session:
        while not stop_event.is_set():
            try:
                url = f"{BINANCE_REST}/fapi/v1/openInterest"
                async with session.get(url, params={"symbol": SYMBOL}, timeout=10) as resp:
                    resp.raise_for_status()
                    data = await resp.json()

                old = store.get_market_state("binance", SYMBOL)
                mark_price = old.mark_price if old else 0.0
                oi_qty = safe_float(data.get("openInterest"))
                state = MarketState(
                    exchange="binance",
                    symbol=SYMBOL,
                    mark_price=mark_price,
                    oi_qty=oi_qty,
                    oi_value_usd=(oi_qty * mark_price if mark_price > 0 else None),
                    funding_rate=(old.funding_rate if old else None),
                    updated_ts=int(data.get("time", now_ms())),
                )
                store.upsert_market_state(state)
            except Exception as e:
                print(f"[binance-oi] error: {e}")

            await asyncio.sleep(interval)

async def run_bybit_ws(store: SQLiteStore, stop_event: asyncio.Event):
    while not stop_event.is_set():
        try:
            async with websockets.connect(BYBIT_WS, ping_interval=20, ping_timeout=20) as ws:
                print("[bybit] connected")
                sub = {
                    "op": "subscribe",
                    "args": [
                        f"tickers.{SYMBOL}",
                        f"allLiquidation.{SYMBOL}",
                    ],
                }
                await ws.send(json.dumps(sub))

                async for raw in ws:
                    if stop_event.is_set():
                        break
                    msg = json.loads(raw)

                    # 忽略 ack / pong
                    if "op" in msg:
                        continue

                    topic = msg.get("topic", "")
                    if topic == f"tickers.{SYMBOL}":
                        row = msg.get("data", {})
                        if isinstance(row, list):
                            row = row[0] if row else {}
                        state = MarketState(
                            exchange="bybit",
                            symbol=row.get("symbol", SYMBOL),
                            mark_price=safe_float(row.get("markPrice")),
                            oi_qty=safe_float(row.get("openInterest"), None),
                            oi_value_usd=safe_float(row.get("openInterestValue"), None),
                            funding_rate=safe_float(row.get("fundingRate"), None),
                            updated_ts=int(msg.get("ts", now_ms())),
                        )
                        store.upsert_market_state(state)

                    elif topic == f"allLiquidation.{SYMBOL}":
                        s = store.get_market_state("bybit", SYMBOL)
                        mark_price = s.mark_price if s else 0.0
                        events = normalize_bybit_all_liq(msg, mark_price)
                        for event in events:
                            store.insert_liquidation_event(event)

        except Exception as e:
            print(f"[bybit] reconnect after error: {e}")
            await asyncio.sleep(2)

async def report_loop(store: SQLiteStore, stop_event: asyncio.Event):
    while not stop_event.is_set():
        try:
            await asyncio.sleep(REPORT_INTERVAL)

            store.cleanup_old_events(RETENTION_MINUTES)

            states = store.list_market_states(SYMBOL)
            if not states:
                continue

            current_price = weighted_current_price(states)
            if current_price is None or current_price <= 0:
                continue

            dist = merged_distribution(states)
            up, down = cumulative_bands(dist, current_price, BANDS)
            band_rows = build_band_rows(current_price, up, down, BANDS)

            short_price, short_notional = longest_bar(dist, side="short", bucket_size=LONGEST_BAR_BUCKET)
            long_price, long_notional = longest_bar(dist, side="long", bucket_size=LONGEST_BAR_BUCKET)

            ts = now_ms()
            store.insert_band_report(ts, SYMBOL, current_price, band_rows)

            if short_price is not None:
                store.insert_longest_bar_report(ts, SYMBOL, "short", LONGEST_BAR_BUCKET, short_price, short_notional)
            if long_price is not None:
                store.insert_longest_bar_report(ts, SYMBOL, "long", LONGEST_BAR_BUCKET, long_price, long_notional)

            events_1m = store.get_recent_events(SYMBOL, 60)
            events_5m = store.get_recent_events(SYMBOL, 300)
            events_15m = store.get_recent_events(SYMBOL, 900)

            # 清屏输出
            try:
                os.system("cls" if os.name == "nt" else "clear")
            except Exception:
                pass

            print_market_states(states)
            print_band_table(current_price, band_rows, (short_price, short_notional), (long_price, long_notional))
            print_realized_block("已发生清算 - 1分钟", events_1m)
            print_realized_block("已发生清算 - 5分钟", events_5m)
            print_realized_block("已发生清算 - 15分钟", events_15m)

            print_divider("SQLite")
            print(f"DB Path: {DB_PATH}")
            print("表：market_state / liquidation_events / band_reports / longest_bar_reports")

        except Exception as e:
            print(f"[report] error: {e}")
            await asyncio.sleep(2)

# =========================
# Main
# =========================

async def main():
    store = SQLiteStore(DB_PATH)
    stop_event = asyncio.Event()

    def _handle_stop(*_):
        print("\n收到退出信号，准备停止...")
        stop_event.set()

    if sys.platform != "win32":
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGINT, signal.SIGTERM):
            try:
                loop.add_signal_handler(sig, _handle_stop)
            except NotImplementedError:
                pass

    tasks = [
        asyncio.create_task(run_binance_mark_ws(store, stop_event)),
        asyncio.create_task(run_binance_force_ws(store, stop_event)),
        asyncio.create_task(poll_binance_open_interest(store, stop_event)),
        asyncio.create_task(run_bybit_ws(store, stop_event)),
        asyncio.create_task(report_loop(store, stop_event)),
    ]

    try:
        await asyncio.gather(*tasks)
    except KeyboardInterrupt:
        stop_event.set()
    finally:
        for t in tasks:
            t.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)

if __name__ == "__main__":
    asyncio.run(main())
