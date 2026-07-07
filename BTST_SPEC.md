# BTST Auto-Trade Engine — Build Spec (v8, locked)

> Single Go binary that scans TWO ChartInk screeners at 3:20 PM (distinct union),
> CARRIES holdings the scan re-listed, squares off the rest, buys the new names —
> each with a 2% TRAILING stop ratcheted by an intraday monitor. Web dashboard.
> Runs 30 days in paper mode, then flips to live.

## Locked decisions (v8)
| # | Decision |
|---|----------|
| Scanners | `ema-reversal-93` + `pvema-3`; top 20 from EACH → distinct union (≤40). One failing screener is tolerated; both failing → square off all + skip buys. |
| Scrape method | **HTTP only** — GET screener page, extract clause (`atlas_query`) + CSRF at runtime, POST `/process`, parse JSON. No headless browser. |
| Carry-over | At 15:20 scan FIRST; holdings re-listed by the scan are NOT sold and NOT re-bought (carry_count++). Holdings that fell off the list are squared off. Sell before buy. |
| Buys | New names only = union − already-held. Market BUY before the 15:30 close. |
| Stop loss | **2% TRAILING**: stop = watermark × 0.98 where watermark = highest price since entry; only ratchets UP. Entry 10 → SL 9.8; peak 12 → SL 11.76. |
| Intraday monitor | Every 5 min (config) during 09:15–15:30: poll quotes, ratchet SL (audit row in sl_events), exit immediately on breach at observed price. Entry-day session high is ignored (pre-ownership); later days use the day high. Restart-safe (reloads from DB). |
| Capital | ₹5L/day ÷ N over NEW buys only; carried positions consume no fresh capital |
| Tracker | scans (outcome traded/carried/dropped/held + source screener + time), positions (peak, last, carry_count), sl_events (every trail step) — full audit |
| Gates / approval | Dormant (`BTST_GATE_ENABLED=false`; `ApproveBuy` hook nil) |
| Mode | Paper (`PAPER_MODE=true`) → live via flag, identical code |
| UI | Dashboard on :8085 — stat cards (holdings/deployed/unrealised/realised/total/win-rate), tabs: Holdings (peak+trail SL+unreal P&L, carry badge) / Today's Scan (source) / Closed / SL Trail / History. Manual ▶ Run (token). |
| Storage | Turso (libSQL) when TURSO_* set; local SQLite fallback |
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
KEEP (reuse): core/auto_login.go, config/config.go (trimmed),
              NSE holiday funcs from main.go
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

## Gates OFF + approval hook
- Automated sentiment gates default OFF (`BTST_GATE_ENABLED=false`); code kept dormant.
- Telegram was fully removed. The engine trades unconditionally at entry time.
- A generic `Engine.ApproveBuy` hook remains (nil = trade unconditionally): the engine
  builds the proposed basket and, if a hook is wired, only places BUYs when it returns
  true. SELL (T+1 square-off) is never gated. The next approval mechanism plugs in here.
- NSE timings (verified): pre-open 09:00–09:15, NORMAL 09:15–15:30 (only window for
  market orders), closing 15:40–16:00. Entry/exit 15:20 sit before the 15:30 close.

## Tier-2 news filter — two layers
1. Keyword hard-block (severe terms + 48h recency) — always on, free.
2. Optional LLM second opinion (`gate/llm.go`, Claude Haiku via raw HTTP) — enabled
   when `BTST_NEWS_LLM=true` and `ANTHROPIC_API_KEY` set. Batches surviving stocks'
   headlines into one call, drops materially-negative names. Fails open on any error.

## Live broker (broker/kite.go) — DORMANT
Implements Broker via Kite Connect v3 REST (market BUY, SL-M, square-off, positions).
Wired in cmd/btst but only selected when PAPER_MODE=false AND Kite creds present.
UNVERIFIED against live API — smoke-test before going live; confirm BTST resting-SL-M
legality on CNC (may need software SL fallback).

## Open verification (before live)
BTST + resting SL-M legality on Zerodha — confirm before flipping PAPER_MODE=false.
Paper mode (and the safe live fallback) uses software-tracked SL. Tested in
engine/exit_test.go (SL-breach, square-off, same-day-hold).
