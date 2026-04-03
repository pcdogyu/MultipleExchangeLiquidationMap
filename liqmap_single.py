#!/usr/bin/env python3
# -*- coding: utf-8 -*-

"""
ETH liquidation map estimator (single-file + SQLite)

功能：
1. 采集 Binance / Bybit / OKX 的 ETHUSDT 实时清算、标记价格、OI
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
    OKX_REST_BASE=https://www.okx.com
    OKX_WS_PUBLIC=wss://ws.okx.com:8443/ws/v5/public
"""

import os
import sys
import argparse
import json
import math
import time
import signal
import contextlib
import sqlite3
import asyncio
import locale
from dataclasses import dataclass
from typing import Optional, Literal, Dict, List, Tuple
from collections import defaultdict
import unicodedata

import aiohttp
import websockets

if os.name == "nt":
    import msvcrt

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
DEFAULT_HEAT_WINDOW_DAYS = 1
HEAT_WINDOW_KEYS = {"1": 1, "7": 7, "3": 30}

BINANCE_WS_MARK = f"wss://fstream.binance.com/ws/{SYMBOL.lower()}@markPrice@1s"
BINANCE_WS_FORCE = f"wss://fstream.binance.com/ws/{SYMBOL.lower()}@forceOrder"
BINANCE_REST = "https://fapi.binance.com"
BYBIT_WS = "wss://stream.bybit.com/v5/public/linear"
OKX_REST_BASE = os.getenv("OKX_REST_BASE", "https://www.okx.com").rstrip("/")
OKX_WS_PUBLIC = os.getenv("OKX_WS_PUBLIC", "wss://ws.okx.com:8443/ws/v5/public")
OKX_INST_ID = os.getenv("OKX_INST_ID", "ETH-USDT-SWAP")

LEVERAGE_BUCKETS = [
    (5, 0.20),
    (10, 0.35),
    (20, 0.30),
    (50, 0.15),
]

ENTRY_OFFSETS = [-0.03, -0.02, -0.01, 0.00, 0.01, 0.02, 0.03]
ENTRY_OFFSET_WEIGHTS = [0.08, 0.12, 0.18, 0.24, 0.18, 0.12, 0.08]

HEAT_WINDOW_DAYS = DEFAULT_HEAT_WINDOW_DAYS

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

def init_utf8_console():
    os.environ.setdefault("PYTHONUTF8", "1")
    os.environ.setdefault("PYTHONIOENCODING", "utf-8")
    with contextlib.suppress(Exception):
        if os.name == "nt":
            os.system("chcp 65001 > NUL")
    for stream_name in ("stdout", "stderr"):
        stream = getattr(sys, stream_name, None)
        if stream is not None and hasattr(stream, "reconfigure"):
            with contextlib.suppress(Exception):
                stream.reconfigure(encoding="utf-8", errors="replace")
    with contextlib.suppress(Exception):
        locale.setlocale(locale.LC_ALL, "")

def display_width(s) -> int:
    s = str(s)
    width = 0
    for ch in s:
        width += 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1
    return width

def pad_cell(value, width: int, align: str = "left") -> str:
    s = str(value)
    pad = max(width - display_width(s), 0)
    if align == "right":
        return " " * pad + s
    if align == "center":
        left = pad // 2
        right = pad - left
        return " " * left + s + " " * right
    return s + " " * pad

def section_width(title: str, minimum: int = 60) -> int:
    return max(display_width(title) + 4, minimum)

def render_section_title(title: str, minimum: int = 60) -> str:
    inner = section_width(title, minimum) - 2
    return "\n".join([
        "╔" + "═" * inner + "╗",
        "║ " + pad_cell(title, inner - 2, "left") + " ║",
        "╚" + "═" * inner + "╝",
    ])

def render_table(headers: List[str], rows: List[List[str]], aligns: Optional[List[str]] = None) -> str:
    if aligns is None:
        aligns = ["left"] * len(headers)

    widths = []
    for i, header in enumerate(headers):
        max_width = display_width(header)
        for row in rows:
            if i < len(row):
                max_width = max(max_width, display_width(row[i]))
        widths.append(max_width)

    top = "┌" + "┬".join("─" * (w + 2) for w in widths) + "┐"
    sep = "├" + "┼".join("─" * (w + 2) for w in widths) + "┤"
    bottom = "└" + "┴".join("─" * (w + 2) for w in widths) + "┘"

    lines = [top]
    header_line = "│ " + " │ ".join(
        pad_cell(headers[i], widths[i], "center") for i in range(len(headers))
    ) + " │"
    lines.append(header_line)
    lines.append(sep)

    for row in rows:
        line = "│ " + " │ ".join(
            pad_cell(row[i] if i < len(row) else "", widths[i], aligns[i])
            for i in range(len(headers))
        ) + " │"
        lines.append(line)

    lines.append(bottom)
    return "\n".join(lines)

def print_subtitle(title: str):
    print(f"\n▸ {title}")

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
            mark_price=CASE
                WHEN excluded.mark_price IS NULL OR excluded.mark_price = 0 THEN market_state.mark_price
                ELSE excluded.mark_price
            END,
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

    def get_event_diag(self, symbol: str) -> dict:
        total = self.conn.execute(
            "SELECT COUNT(*) AS c FROM liquidation_events WHERE symbol=?",
            (symbol,)
        ).fetchone()["c"]
        row = self.conn.execute("""
        SELECT exchange, side, price, qty, notional_usd, event_ts
        FROM liquidation_events
        WHERE symbol=?
        ORDER BY event_ts DESC
        LIMIT 1
        """, (symbol,)).fetchone()
        return {
            "total": int(total or 0),
            "last_exchange": row["exchange"] if row else None,
            "last_side": row["side"] if row else None,
            "last_price": row["price"] if row else None,
            "last_qty": row["qty"] if row else None,
            "last_notional_usd": row["notional_usd"] if row else None,
            "last_event_ts": row["event_ts"] if row else None,
        }

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

    def get_band_report_snapshot(self, symbol: str, lookback_days: int) -> List[dict]:
        cutoff = now_ms() - lookback_days * 24 * 60 * 60 * 1000
        rows = self.conn.execute("""
        SELECT band,
               MAX(report_ts) AS latest_ts,
               AVG(current_price) AS avg_current_price,
               AVG(up_notional_usd) AS avg_up_notional_usd,
               AVG(down_notional_usd) AS avg_down_notional_usd
        FROM band_reports
        WHERE symbol=? AND report_ts>=?
        GROUP BY band
        ORDER BY band
        """, (symbol, cutoff)).fetchall()
        out = []
        for row in rows:
            out.append({
                "band": int(row["band"]),
                "latest_ts": int(row["latest_ts"] or 0),
                "current_price": float(row["avg_current_price"] or 0.0),
                "up_notional_usd": float(row["avg_up_notional_usd"] or 0.0),
                "down_notional_usd": float(row["avg_down_notional_usd"] or 0.0),
            })
        return out

    def get_longest_bar_snapshot(self, symbol: str, lookback_days: int) -> dict:
        cutoff = now_ms() - lookback_days * 24 * 60 * 60 * 1000
        rows = self.conn.execute("""
        SELECT side, bucket_price, AVG(bucket_notional_usd) AS avg_bucket_notional_usd
        FROM longest_bar_reports
        WHERE symbol=? AND report_ts>=?
        GROUP BY side, bucket_price
        """, (symbol, cutoff)).fetchall()
        out = {"short": {"price": None, "notional": 0.0}, "long": {"price": None, "notional": 0.0}}
        best: Dict[str, Dict[str, float]] = {}
        for row in rows:
            side = row["side"]
            bucket_price = float(row["bucket_price"] or 0.0)
            notional = float(row["avg_bucket_notional_usd"] or 0.0)
            prev = best.get(side)
            if prev is None or notional > prev["notional"]:
                best[side] = {"price": bucket_price, "notional": notional}
        for side in out:
            if side in best:
                out[side] = best[side]
        return out

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


def okx_contract_usd_per_contract(state: Optional[MarketState], fallback_price: float = 0.0) -> float:
    if state and state.oi_qty and state.oi_qty > 0 and state.oi_value_usd and state.oi_value_usd > 0:
        return state.oi_value_usd / state.oi_qty
    return fallback_price if fallback_price > 0 else 0.0

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
    if title:
        print()
        print(render_section_title(title))
    else:
        print()

def print_market_states(states: List[MarketState]):
    print_divider("市场状态")
    headers = ["交易所", "标记价", "OI数量", "OI价值USD", "Funding", "更新时间"]
    rows = []
    for s in states:
        rows.append([
            s.exchange,
            fmt_price(s.mark_price),
            f"{s.oi_qty:.4f}" if s.oi_qty is not None else "-",
            fmt_usd(s.oi_value_usd) if s.oi_value_usd is not None else "-",
            f"{s.funding_rate:.6f}" if s.funding_rate is not None else "-",
            utc_ts_str(s.updated_ts),
        ])
    print(render_table(headers, rows, aligns=["left", "right", "right", "right", "right", "right"]))

def print_realized_block(title: str, events: List[LiquidationEvent]):
    print_divider(title)
    summary = realized_summary(events)

    if not summary["rows"]:
        print(render_table(["状态"], [["暂无数据"]], aligns=["center"]))
        return

    print_subtitle("汇总")
    summary_rows = []
    for row in summary["rows"]:
        summary_rows.append([
            row["exchange"],
            row["side"],
            fmt_usd(row["notional_usd"]),
        ])
    print(render_table(
        ["交易所", "被清算方向", "名义价值"],
        summary_rows,
        aligns=["left", "left", "right"],
    ))

    print_subtitle("最近大额事件")
    detail_rows = []
    for e in sorted(events, key=lambda x: x.notional_usd, reverse=True)[:PRINT_TOP_N_EVENTS]:
        detail_rows.append([
            utc_ts_str(e.event_ts),
            e.exchange,
            e.side,
            fmt_price(e.price),
            f"{e.qty:.4f}",
            fmt_usd(e.notional_usd),
        ])
    print(render_table(
        ["时间", "交易所", "方向", "价格", "数量", "名义价值"],
        detail_rows,
        aligns=["left", "left", "left", "right", "right", "right"],
    ))

def print_band_table(current_price: float, rows: List[dict], longest_short: Tuple[Optional[float], float], longest_long: Tuple[Optional[float], float]):
    print_divider(f"{SYMBOL} 清算热区速报  当前价={fmt_price(current_price)}")

    band_rows = []
    for row in rows:
        band_rows.append([
            f"{row['band']}点内",
            fmt_price(row['up_price']),
            fmt_usd(row['up_notional_usd']),
            fmt_price(row['down_price']),
            fmt_usd(row['down_notional_usd']),
        ])

    print(render_table(
        ["点数阈值", "上方空单价", "上方空单规模", "下方多单价", "下方多单规模"],
        band_rows,
        aligns=["left", "right", "right", "right", "right"],
    ))

    short_price, short_notional = longest_short
    long_price, long_notional = longest_long

    print_subtitle("最长柱")
    print(render_table(
        ["方向", "价格", "规模"],
        [
            ["上方空单", fmt_price(short_price) if short_price is not None else "-", fmt_usd(short_notional)],
            ["下方多单", fmt_price(long_price) if long_price is not None else "-", fmt_usd(long_notional)],
        ],
        aligns=["left", "right", "right"],
    ))


def print_event_diag(diag: dict):
    print_divider("清算流诊断")
    print(render_table(
        ["指标", "值"],
        [
            ["累计事件数", str(diag.get("total", 0))],
            ["最近事件交易所", diag.get("last_exchange") or "-"],
            ["最近事件方向", diag.get("last_side") or "-"],
            ["最近事件价格", fmt_price(diag.get("last_price")) if diag.get("last_price") is not None else "-"],
            ["最近事件数量", f"{diag.get('last_qty'):.4f}" if diag.get("last_qty") is not None else "-"],
            ["最近事件名义价值", fmt_usd(diag.get("last_notional_usd")) if diag.get("last_notional_usd") is not None else "-"],
            ["最近事件时间", utc_ts_str(diag.get("last_event_ts")) if diag.get("last_event_ts") is not None else "-"],
        ],
        aligns=["left", "left"],
    ))

def print_band_snapshot_table(lookback_days: int, rows: List[dict], longest_bar_snapshot: dict):
    print_divider(f"{SYMBOL} ?????? - {lookback_days}?")
    if not rows:
        print(render_table(["??"], [["????????"]], aligns=["center"]))
        return

    band_rows = []
    for row in rows:
        band_rows.append([
            f"{row['band']}??",
            fmt_usd(row["up_notional_usd"]),
            fmt_usd(row["down_notional_usd"]),
        ])
    print(render_table(
        ["????", "??????", "??????"],
        band_rows,
        aligns=["left", "right", "right"],
    ))

def current_heat_window_days() -> int:
    return HEAT_WINDOW_DAYS

def set_heat_window_days(value: int):
    global HEAT_WINDOW_DAYS
    HEAT_WINDOW_DAYS = value
    print_subtitle("?????")
    print(render_table(
        ["??", "??", "??"],
        [
            ["????", fmt_price(longest_bar_snapshot["short"]["price"]) if longest_bar_snapshot["short"]["price"] is not None else "-", fmt_usd(longest_bar_snapshot["short"]["notional"])],
            ["????", fmt_price(longest_bar_snapshot["long"]["price"]) if longest_bar_snapshot["long"]["price"] is not None else "-", fmt_usd(longest_bar_snapshot["long"]["notional"])],
        ],
        aligns=["left", "right", "right"],
    ))


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


def normalize_okx_liquidation_orders(msg: dict, market_state: Optional[MarketState]) -> List[LiquidationEvent]:
    data = msg.get("data", [])
    rows = data if isinstance(data, list) else [data]
    out = []

    mark_price = market_state.mark_price if market_state else 0.0
    contract_usd = okx_contract_usd_per_contract(market_state, mark_price)

    for row in rows:
        if row.get("instId") != OKX_INST_ID:
            continue

        details = row.get("details", [])
        detail_rows = details if isinstance(details, list) else [details]
        for detail in detail_rows:
            pos_side = str(detail.get("posSide", "")).lower()
            raw_side = str(detail.get("side", "")).lower()

            if pos_side in ("long", "short"):
                side = pos_side
            else:
                side = "short" if raw_side == "buy" else "long"

            qty = safe_float(detail.get("sz"))
            price = safe_float(detail.get("bkPx"), mark_price)
            per_contract_usd = contract_usd if contract_usd > 0 else price
            notional_usd = qty * per_contract_usd

            out.append(LiquidationEvent(
                exchange="okx",
                symbol=SYMBOL,
                side=side,
                raw_side=f"{pos_side}:{raw_side}",
                qty=qty,
                price=price,
                mark_price=mark_price if mark_price > 0 else price,
                notional_usd=notional_usd,
                event_ts=int(safe_float(detail.get("ts"), now_ms())),
            ))

    return out

# =========================
# Async tasks
# =========================

async def run_binance_mark_ws(store: SQLiteStore, stop_event: asyncio.Event):
    while not stop_event.is_set():
        ws = None
        try:
            ws = await websockets.connect(BINANCE_WS_MARK, ping_interval=20, ping_timeout=20)
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
        except asyncio.CancelledError:
            break
        except Exception as e:
            print(f"[binance-mark] reconnect after error: {e}")
            await asyncio.sleep(2)
        finally:
            if ws is not None:
                with contextlib.suppress(Exception):
                    await ws.close()

async def run_binance_force_ws(store: SQLiteStore, stop_event: asyncio.Event):
    while not stop_event.is_set():
        ws = None
        try:
            ws = await websockets.connect(BINANCE_WS_FORCE, ping_interval=20, ping_timeout=20)
            print("[binance-force] connected")
            async for raw in ws:
                if stop_event.is_set():
                    break
                msg = json.loads(raw)
                if msg.get("e") != "forceOrder":
                    continue

                s = store.get_market_state("binance", SYMBOL)
                mark_price = s.mark_price if s else 0.0
                event = normalize_binance_force_order(msg, mark_price)
                store.insert_liquidation_event(event)
        except asyncio.CancelledError:
            break
        except Exception as e:
            print(f"[binance-force] reconnect after error: {e}")
            await asyncio.sleep(2)
        finally:
            if ws is not None:
                with contextlib.suppress(Exception):
                    await ws.close()

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
        ws = None
        try:
            ws = await websockets.connect(BYBIT_WS, ping_interval=20, ping_timeout=20)
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

                    old = store.get_market_state("bybit", SYMBOL)
                    old_mark = old.mark_price if old else 0.0
                    old_oi_qty = old.oi_qty if old else None
                    old_oi_value = old.oi_value_usd if old else None
                    old_funding = old.funding_rate if old else None

                    state = MarketState(
                        exchange="bybit",
                        symbol=row.get("symbol", SYMBOL),
                        mark_price=safe_float(row.get("markPrice"), old_mark),
                        oi_qty=safe_float(row.get("openInterest"), old_oi_qty),
                        oi_value_usd=safe_float(row.get("openInterestValue"), old_oi_value),
                        funding_rate=safe_float(row.get("fundingRate"), old_funding),
                        updated_ts=int(msg.get("ts", now_ms())),
                    )
                    store.upsert_market_state(state)

                elif topic == f"allLiquidation.{SYMBOL}":
                    s = store.get_market_state("bybit", SYMBOL)
                    mark_price = s.mark_price if s else 0.0
                    events = normalize_bybit_all_liq(msg, mark_price)
                    for event in events:
                        store.insert_liquidation_event(event)

        except asyncio.CancelledError:
            break
        except Exception as e:
            print(f"[bybit] reconnect after error: {e}")
            await asyncio.sleep(2)
        finally:
            if ws is not None:
                with contextlib.suppress(Exception):
                    await ws.close()




async def run_okx_ws(store: SQLiteStore, stop_event: asyncio.Event):
    while not stop_event.is_set():
        ws = None
        try:
            ws = await websockets.connect(OKX_WS_PUBLIC, ping_interval=20, ping_timeout=20)
            print("[okx] connected")
            sub = {
                "op": "subscribe",
                "args": [
                    {"channel": "mark-price", "instId": OKX_INST_ID},
                    {"channel": "tickers", "instId": OKX_INST_ID},
                    {"channel": "liquidation-orders", "instType": "SWAP"},
                ],
            }
            await ws.send(json.dumps(sub))

            async for raw in ws:
                if stop_event.is_set():
                    break

                if raw == "pong":
                    continue

                msg = json.loads(raw)

                if msg.get("event") in {"subscribe", "unsubscribe"}:
                    continue
                if msg.get("event") == "error":
                    print(f"[okx] ws error: {msg}")
                    continue

                arg = msg.get("arg", {})
                channel = arg.get("channel")
                data = msg.get("data", [])
                rows = data if isinstance(data, list) else [data]
                if not rows:
                    continue

                if channel == "liquidation-orders":
                    state = store.get_market_state("okx", SYMBOL)
                    events = normalize_okx_liquidation_orders(msg, state)
                    for event in events:
                        store.insert_liquidation_event(event)
                    continue

                old = store.get_market_state("okx", SYMBOL)
                old_mark = old.mark_price if old else 0.0
                old_oi_qty = old.oi_qty if old else None
                old_oi_value = old.oi_value_usd if old else None
                old_funding = old.funding_rate if old else None

                for row in rows:
                    mark_price = old_mark
                    if channel == "mark-price":
                        mark_price = safe_float(row.get("markPx"), old_mark)
                    elif channel == "tickers":
                        mark_price = safe_float(
                            row.get("markPx"),
                            safe_float(row.get("last"), old_mark)
                        )

                    state = MarketState(
                        exchange="okx",
                        symbol=SYMBOL,
                        mark_price=mark_price,
                        oi_qty=old_oi_qty,
                        oi_value_usd=old_oi_value,
                        funding_rate=old_funding,
                        updated_ts=int(safe_float(row.get("ts"), msg.get("ts", now_ms()))),
                    )
                    store.upsert_market_state(state)
        except asyncio.CancelledError:
            break
        except Exception as e:
            print(f"[okx] reconnect after error: {e}")
            await asyncio.sleep(2)
        if ws is not None:
            with contextlib.suppress(Exception):
                await ws.close()


async def poll_okx_public_metrics(store: SQLiteStore, stop_event: asyncio.Event, interval: int = 10):
    async with aiohttp.ClientSession() as session:
        while not stop_event.is_set():
            try:
                old = store.get_market_state("okx", SYMBOL)
                mark_price = old.mark_price if old else 0.0
                old_oi_qty = old.oi_qty if old else None
                old_oi_value = old.oi_value_usd if old else None
                old_funding = old.funding_rate if old else None

                oi_qty = old_oi_qty
                oi_value_usd = old_oi_value
                funding_rate = old_funding
                updated_ts = now_ms()

                oi_url = f"{OKX_REST_BASE}/api/v5/public/open-interest"
                oi_params = {"instType": "SWAP", "instId": OKX_INST_ID}
                async with session.get(oi_url, params=oi_params, timeout=10) as resp:
                    resp.raise_for_status()
                    data = await resp.json()
                oi_rows = data.get("data", []) if isinstance(data, dict) else []
                if oi_rows:
                    row = oi_rows[0]
                    oi_qty = safe_float(row.get("oi"), old_oi_qty)
                    oi_value_usd = safe_float(row.get("oiUsd"), old_oi_value)
                    updated_ts = int(safe_float(row.get("ts"), updated_ts))

                fr_url = f"{OKX_REST_BASE}/api/v5/public/funding-rate"
                fr_params = {"instId": OKX_INST_ID}
                async with session.get(fr_url, params=fr_params, timeout=10) as resp:
                    resp.raise_for_status()
                    data = await resp.json()
                fr_rows = data.get("data", []) if isinstance(data, dict) else []
                if fr_rows:
                    fr_row = fr_rows[0]
                    funding_rate = safe_float(fr_row.get("fundingRate"), old_funding)
                    updated_ts = int(safe_float(fr_row.get("ts"), updated_ts))

                state = MarketState(
                    exchange="okx",
                    symbol=SYMBOL,
                    mark_price=mark_price,
                    oi_qty=oi_qty,
                    oi_value_usd=oi_value_usd,
                    funding_rate=funding_rate,
                    updated_ts=updated_ts,
                )
                store.upsert_market_state(state)
            except Exception as e:
                print(f"[okx-rest] error: {e}")

            await asyncio.sleep(interval)

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

            heat_window_days = current_heat_window_days()
            snapshot_rows = store.get_band_report_snapshot(SYMBOL, heat_window_days)
            longest_snapshot = store.get_longest_bar_snapshot(SYMBOL, heat_window_days)
            print_band_snapshot_table(heat_window_days, snapshot_rows, longest_snapshot)

            print_event_diag(store.get_event_diag(SYMBOL))
            print_realized_block("已发生清算 - 1分钟", events_1m)
            print_realized_block("已发生清算 - 5分钟", events_5m)
            print_realized_block("已发生清算 - 15分钟", events_15m)

            print_divider("SQLite")
            print(render_table(
                ["项目", "值"],
                [
                    ["DB Path", DB_PATH],
                    ["数据表", "market_state / liquidation_events / band_reports / longest_bar_reports"],
                    ["OKX合约", OKX_INST_ID],
                ],
                aligns=["left", "left"],
            ))

        except Exception as e:
            print(f"[report] error: {e}")
            await asyncio.sleep(2)

# =========================
# Main
# =========================

async def keyboard_loop(stop_event: asyncio.Event):
    if os.name != "nt":
        return
    while not stop_event.is_set():
        try:
            if msvcrt.kbhit():
                ch = msvcrt.getwch()
                if ch in ("1", "7", "3"):
                    set_heat_window_days(HEAT_WINDOW_KEYS[ch])
                    print(f"\\n[heat-window] 已切换到 {current_heat_window_days()} 天")
                elif ch in ("q", "Q"):
                    print("\\n[heat-window] 收到退出命令，准备停止...")
                    stop_event.set()
                    break
            await asyncio.sleep(0.1)
        except asyncio.CancelledError:
            break


async def main():
    init_utf8_console()
    store = SQLiteStore(DB_PATH)
    stop_event = asyncio.Event()

    def _handle_stop(*_):
        print("\\n收到退出信号，准备停止...")
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
        asyncio.create_task(run_okx_ws(store, stop_event)),
        asyncio.create_task(poll_okx_public_metrics(store, stop_event)),
        asyncio.create_task(report_loop(store, stop_event)),
        asyncio.create_task(keyboard_loop(stop_event)),
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
