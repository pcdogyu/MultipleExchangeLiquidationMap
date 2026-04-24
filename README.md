# ETH Liquidation Monitor / ETH 清算监控脚本

## English

### Overview
This project is a **Go application with a web dashboard** for monitoring ETH derivatives liquidation activity and estimating nearby liquidation pressure.

It currently integrates data from:

- **Binance**
- **Bybit**
- **OKX**

The script collects public market data, stores it in SQLite, and prints a terminal dashboard that includes:

- current market state by exchange
- realized liquidation statistics for the last **1 min / 5 min / 15 min**
- estimated liquidation heat zones above and below the current ETH price
- largest estimated liquidation bar
- a simple stream diagnostics panel

### Important Notes
- **Realized liquidation** = liquidation events that have already happened and were received from exchange public streams.
- **Estimated liquidation heat zone** = a model-based estimate built from mark price, open interest, and a simplified leverage distribution. It is **not** the exchange’s internal liquidation ledger.
- If the “recent liquidation” section is empty, it usually means:
  - no liquidation event was received during that time window, or
  - the exchange stream was connected but no new event was pushed during that period.

---

### Features
- Single-file deployment
- SQLite local storage
- Terminal dashboard with table-style layout
- Multi-exchange market state aggregation
- Realized liquidation event collection
- Estimated liquidation band calculation
- OKX support for market state and liquidation events
- Adjustable runtime parameters via environment variables

---

### Data Stored in SQLite
The script creates these tables automatically:

- `market_state`  
  Latest market state by exchange and symbol
- `liquidation_events`  
  Realized liquidation events collected from exchange streams
- `band_reports`  
  Periodic estimated liquidation band snapshots
- `longest_bar_reports`  
  Periodic largest estimated liquidation bar snapshots

---

### Requirements
- Go 1.21+
- Internet access
- Recommended OS:
  - Linux
  - macOS
  - Windows Terminal / PowerShell

Install dependencies:

```bash
pip install aiohttp websockets
```

Or with a requirements file:

```bash
pip install -r requirements.txt
```

---

### Files
Main script:

```bash
liqmap_single_okx_fixed.py
```

Optional generated database:

```bash
data/liqmap.db
```

---

### How to Run
Run with default settings:

```bash
go run .
```

Run with debug logging:

```bash
DEBUG=1 DEBUG_LOG=log/server.log go run .
```

Run with custom environment variables:

```bash
SYMBOL=ETHUSDT \
DB_PATH=data/liqmap.db \
REPORT_INTERVAL=5 \
RETENTION_MINUTES=240 \
OKX_REST_BASE=https://www.okx.com \
OKX_WS_PUBLIC=wss://ws.okx.com:8443/ws/v5/public \
OKX_INST_ID=ETH-USDT-SWAP \
go run .
```

Windows PowerShell example:

```powershell
$env:SYMBOL="ETHUSDT"
$env:DB_PATH="data/liqmap.db"
$env:DEBUG="1"
$env:DEBUG_LOG="log/server.log"
$env:REPORT_INTERVAL="5"
$env:RETENTION_MINUTES="240"
$env:OKX_REST_BASE="https://www.okx.com"
$env:OKX_WS_PUBLIC="wss://ws.okx.com:8443/ws/v5/public"
$env:OKX_INST_ID="ETH-USDT-SWAP"
go run .
```

---

### Environment Variables
| Variable | Default | Description |
|---|---|---|
| `SYMBOL` | `ETHUSDT` | Symbol displayed in the dashboard |
| `DB_PATH` | `data/liqmap.db` | SQLite database path |
| `DEBUG` | unset | Set to `1` or `true` to enable debug logging |
| `DEBUG_LOG` | `log/server.log` | Debug log output file when `DEBUG` is enabled |
| `REPORT_INTERVAL` | `5` | Dashboard refresh interval in seconds |
| `RETENTION_MINUTES` | `240` | Retention window for `liquidation_events` cleanup |
| `OKX_REST_BASE` | `https://www.okx.com` | OKX REST base URL |
| `OKX_WS_PUBLIC` | `wss://ws.okx.com:8443/ws/v5/public` | OKX public WebSocket URL |
| `OKX_INST_ID` | `ETH-USDT-SWAP` | OKX instrument ID used for swap data |

---

### What You Will See in the Terminal
The dashboard contains several sections:

1. **Market State**  
   Mark price, open interest, open interest USD value, funding rate, and update time by exchange.

2. **Estimated Liquidation Heat Report**  
   Estimated liquidation size within price bands such as 10 / 20 / 30 / 50 / 100 / 150 points above and below current price.

3. **Stream Diagnostics**  
   Total collected events, latest event exchange, side, price, size, and timestamp.

4. **Realized Liquidation Statistics**  
   Statistics for the last 1 minute, 5 minutes, and 15 minutes.

5. **SQLite Info**  
   Current DB path and table names.

---

### Troubleshooting

#### 1. Bybit mark price shows 0 or empty
This is usually caused by ticker delta messages not carrying all fields every time. Use the latest fixed script version.

#### 2. Recent liquidation sections are empty
Possible reasons:
- no liquidation happened within the recent window
- stream connection is alive but no new liquidation message arrived
- you just started the script and the database is still empty

#### 3. OKX connection errors
Check:
- `OKX_WS_PUBLIC`
- `OKX_REST_BASE`
- your network or region restrictions
- whether the selected `OKX_INST_ID` is valid

#### 4. SQLite file grows over time
This script only cleans old rows from `liquidation_events`.  
If needed, you can manually clean or archive old rows from `band_reports` and `longest_bar_reports`.

---

### Suggested Next Improvements
- add text `ping/pong` keepalive for OKX
- export terminal data to PNG
- add FastAPI / REST API output
- add Telegram or Feishu push notifications
- support more symbols
- add CSV export

---

## 中文

### 项目简介
这是一个**单文件 Python 终端程序**，用于监控 ETH 衍生品的清算情况，并估算当前价格附近的潜在清算压力。

目前已接入：

- **Binance**
- **Bybit**
- **OKX**

脚本会采集公开市场数据，落库到 SQLite，并在终端输出一个表格式监控面板，包含：

- 各交易所市场状态
- 最近 **1 分钟 / 5 分钟 / 15 分钟** 已发生清算统计
- ETH 当前价格上下区间的预估清算热区
- 最大预估清算柱
- 简单的流诊断面板

### 重要说明
- **已发生清算**：指交易所公开流里已经发生并被脚本收到的清算事件。
- **预估清算热区**：是基于标记价格、未平仓量、简化杠杆分布模型做出的估算，**不是交易所内部真实清算底账**。
- 如果“最近清算统计”为空，通常表示：
  - 该时间窗口内没有收到新的清算事件；
  - 或者流是连着的，但最近没有新的事件推送。

---

### 功能特性
- 单文件部署
- SQLite 本地存储
- 终端表格式面板
- 多交易所市场状态聚合
- 已发生清算事件采集
- 预估清算区间计算
- 支持 OKX 市场状态与清算事件
- 通过环境变量调整运行参数

---

### SQLite 数据表
脚本会自动创建以下数据表：

- `market_state`  
  各交易所当前市场状态
- `liquidation_events`  
  已发生清算事件
- `band_reports`  
  周期性保存的预估清算区间快照
- `longest_bar_reports`  
  周期性保存的最大预估清算柱快照

---

### 运行要求
- Go 1.21+
- 可访问互联网
- 推荐系统：
  - Linux
  - macOS
  - Windows Terminal / PowerShell

安装依赖：

```bash
pip install aiohttp websockets
```

或者：

```bash
pip install -r requirements.txt
```

---

### 文件说明
主程序：

```bash
liqmap_single_okx_fixed.py
```

运行后可能生成数据库文件：

```bash
data/liqmap.db
```

---

### 如何运行
使用默认参数直接运行：

```bash
go run .
```

使用自定义环境变量运行：

```bash
SYMBOL=ETHUSDT \
DB_PATH=data/liqmap.db \
REPORT_INTERVAL=5 \
RETENTION_MINUTES=240 \
OKX_REST_BASE=https://www.okx.com \
OKX_WS_PUBLIC=wss://ws.okx.com:8443/ws/v5/public \
OKX_INST_ID=ETH-USDT-SWAP \
go run .
```

Windows PowerShell 示例：

```powershell
$env:SYMBOL="ETHUSDT"
$env:DB_PATH="data/liqmap.db"
$env:REPORT_INTERVAL="5"
$env:RETENTION_MINUTES="240"
$env:OKX_REST_BASE="https://www.okx.com"
$env:OKX_WS_PUBLIC="wss://ws.okx.com:8443/ws/v5/public"
$env:OKX_INST_ID="ETH-USDT-SWAP"
python .\liqmap_single_okx_fixed.py
```

---

### 环境变量说明
| 变量名 | 默认值 | 说明 |
|---|---|---|
| `SYMBOL` | `ETHUSDT` | 终端展示的交易对 |
| `DB_PATH` | `data/liqmap.db` | SQLite 数据库路径 |
| `REPORT_INTERVAL` | `5` | 面板刷新间隔，单位秒 |
| `RETENTION_MINUTES` | `240` | `liquidation_events` 清理保留时长，单位分钟 |
| `OKX_REST_BASE` | `https://www.okx.com` | OKX REST 地址 |
| `OKX_WS_PUBLIC` | `wss://ws.okx.com:8443/ws/v5/public` | OKX 公共 WebSocket 地址 |
| `OKX_INST_ID` | `ETH-USDT-SWAP` | OKX 永续合约 ID |

---

### 终端输出内容
终端面板主要包括：

1. **市场状态**  
   各交易所标记价格、未平仓量、未平仓美元价值、资金费率、更新时间。

2. **清算热区速报**  
   当前价格上下 10 / 20 / 30 / 50 / 100 / 150 点范围内的预估清算规模。

3. **清算流诊断**  
   累计事件数、最近一笔事件的交易所、方向、价格、数量和时间。

4. **已发生清算统计**  
   最近 1 分钟、5 分钟、15 分钟的清算统计信息。

5. **SQLite 信息**  
   当前数据库路径和数据表名称。

---

### 常见问题排查

#### 1. Bybit 标记价格显示为空或为 0
通常是 ticker delta 消息不带全部字段导致。请使用修复后的最新版脚本。

#### 2. 最近清算统计为空
常见原因：
- 最近时间窗口内没有发生新的清算；
- 流虽然连着，但最近没有新的清算推送；
- 脚本刚启动，数据库里还没有事件。

#### 3. OKX 连接报错
请检查：
- `OKX_WS_PUBLIC`
- `OKX_REST_BASE`
- 网络或区域限制
- `OKX_INST_ID` 是否有效

#### 4. SQLite 文件越来越大
当前脚本只会清理 `liquidation_events` 里的旧数据。  
如果需要，你可以手动清理或归档 `band_reports` 和 `longest_bar_reports`。

---

### 后续可扩展方向
- 为 OKX 增加文本 `ping/pong` 保活
- 导出 PNG 报表
- 增加 FastAPI / REST API
- 增加 Telegram / 飞书推送
- 支持更多交易对
- 增加 CSV 导出
