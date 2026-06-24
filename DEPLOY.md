# Deploying the BTST Engine

## What runs
A single Go binary (`cmd/btst`) that:
- serves the dashboard on `$PORT` (default 8085),
- every trading day at `BTST_ENTRY_TIME` (default 15:20 IST) squares off the prior
  day's positions, then scrapes `pur-ema10-20` and places equal-weight ₹5L/N trades,
- reports to Telegram and persists to SQLite.

## Render (free tier)
1. Push this repo to GitHub.
2. In Render: **New → Blueprint**, point at this repo. It reads `render.yaml`.
3. Set the secret env vars (not in the blueprint): `TELEGRAM_BOT_TOKEN`,
   `TELEGRAM_CHAT_IDS`, and — only for live mode — the `KITE_*` / `ZERODHA_*` creds.
4. Deploy. Confirm the dashboard URL loads and Telegram gets the "online" message.

`PAPER_MODE=true` is the default. **Do not flip to `false` until the 30-day paper
run is reviewed.** (Live order placement via KiteBroker is a later phase; until then
the engine runs paper regardless of the flag.)

## Keep-alive (critical)
Render's free tier sleeps after ~15 min idle. A sleeping process misses the 15:20
trigger. Fix with **UptimeRobot** (free):
1. Create an HTTP(s) monitor pointing at your Render dashboard URL.
2. Interval: 5 minutes.
This pings `/` continuously so the box never sleeps before 15:20.

> Note: even with keep-alive, free tiers can be evicted. For real-money live mode,
> use Render's paid Starter plan or a small always-on VPS. Paper mode on free is fine.

## Time zone
The container sets `tzdata`; the engine computes everything in IST internally
(`config.NowIST`), so the host time zone does not matter.

## Local run
```
go run ./cmd/btst        # uses ./.env, serves :8085
go run ./cmd/dashboard   # dashboard only, with seeded demo data
```
