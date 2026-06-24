# BTST Auto-Trade Engine — Build Spec (v6, locked)

> Single Go binary that scans ChartInk `pur-ema10-20` at 3:20 PM, places equal-weight
> BTST trades, squares off next day at 3:20 PM, with a web dashboard + Telegram reports.
> Runs 30 days in paper mode, then flips to live.

## Locked decisions
| # | Decision |
|---|----------|
| Scanner | ChartInk `pur-ema10-20` only (all book scanners removed) |
| Merge | C# scraper ported into Go → one binary |
| Scrape method | **HTTP only** — GET screener page, extract `scan_clause` + CSRF at runtime, POST `/process`, parse JSON. No headless browser. |
| Strategy | Pure BTST: buy 3:20 PM, sell next trading day 3:20 PM |
| Scan time | 3:20 PM live (accept provisional daily candle) |
| Entry | Market BUY + SL handling |
| Stop loss | Configurable % (default 6.5%), software-tracked in paper |
| Exit | Market SELL next trading day 3:20 PM; skip if SL already hit |
| Capital | ₹5L fixed per day ÷ N stocks equal weight (N ≤ 20) |
| Capital cycle | T+1 sale proceeds NOT reused same day; fresh ₹5L/day (₹10L total split) |
| Macro gate (Tier 1) | India VIX + GIFT Nifty + Nifty intraday → skip whole day if bad |
| News gate (Tier 2) | Per-stock keyword filter → drop bad names, trade the rest |
| Neutral day | TRADE |
| Mode | Paper 30 days (`PAPER_MODE=true`) → live via flag, identical code |
| UI | Web dashboard on :8085 (trades, positions, P&L, win rate) |
| Telegram | Entry / exit / skip reports, both modes, [PAPER] label |
| Repo | Clean rebuild in existing repo; reuse 5 working pieces |
| Deploy | Render (free) + UptimeRobot keep-alive |

## ChartInk HTTP recipe (validated 2026-06-24)
1. `GET https://chartink.com/screener/pur-ema10-20` (User-Agent: Mozilla) → keep cookie jar.
2. Extract CSRF: `<meta name="csrf-token" content="...">`.
3. Extract clause: embedded JSON `scan_clause&quot;:&quot;...&quot;` (HTML-unescape).
4. `POST https://chartink.com/screener/process` with cookie jar +
   header `x-csrf-token` + form `scan_clause=<clause>`.
5. Response JSON: `data:[{nsecode, name, close, per_chg, volume}]`.
   Filter rows where `bsecode == null` (indices). Take ≤20.

## File layout
```
KEEP (reuse): core/auto_login.go, agents/telegram.go,
              config/config.go (trimmed), NSE holiday funcs from main.go
NEW:  scanner/chartink.go      — HTTP scraper
      broker/broker.go         — Broker interface
      broker/paper.go          — simulated fills + software SL
      broker/kite.go           — live orders (P6)
      gate/macro.go            — VIX + GIFT Nifty + Nifty intraday
      gate/news.go             — per-stock keyword filter
      engine/btst.go           — 3:20 entry + next-day exit orchestrator
      store/positions.go       — SQLite open/closed trades + P&L
      web/server.go + ui/      — dashboard
      cmd/btst/main.go         — entrypoint
DELETE: agents/{eod_scanner,strategy_ema,scanner_agent,signal_engine,
        signal_alert_agent,market_regime,earnings_watch_agent}.go,
        research/*, storage/kite_websocket.go, scratch/*,
        ~30 book constants in config.go
```

## Broker interface (paper/live switch)
```go
type Broker interface {
    PlaceMarketBuy(symbol string, qty int) (orderID string, fillPrice float64, err error)
    PlaceSLM(symbol string, qty int, trigger float64) (orderID string, err error)
    SquareOff(symbol string, qty int) (fillPrice float64, err error)
    OpenPositions() ([]Position, error)
}
```
Software SL (paper + safe live pattern): next morning, if day low ≤ SL price,
record exit at SL price; else hold to 3:20 PM square-off.

## Daily cycle
```
3:20:00 Tier-1 macro gate → bad? skip report, done
3:20:05 scrape pur-ema10-20 → ≤20 tickers
3:20:30 Tier-2 news filter → qty = 5L / N → BUY (+SL) → persist → entry report
next-day 3:20 square off open positions (skip already-SL-hit) → P&L → exit report
always  dashboard on :8085
```

## Build phases
P1 scraper → P2 paper broker + entry → P3 exit + P&L store →
P4 gates → P5 dashboard → P6 live Kite + deploy.

## Open verification (non-blocking for paper)
BTST + resting SL-M legality on Zerodha — confirm in P6. Paper uses software SL.
