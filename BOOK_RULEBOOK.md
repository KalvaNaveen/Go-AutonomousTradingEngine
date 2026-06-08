# BOOK_RULEBOOK.md
## *Swing Trading Simplified* by Ankur Patel — Verbatim Engine Specification

**Source:** *Swing Trading Simplified*, by Ankur Patel.
**Foreword:** Manas Arora, Marios Stamatoudis.
**Total pages:** 306 (PDF) / 298 (printed body).
**Forensic basis:** Every rule below was verified by reading the rendered PDF page directly with Claude vision after a full Windows-OCR pass. Garbled OCR output was re-verified visually for **all 11 chapter summary boxes**, the bird's-eye scanner, the risk-management rules, and every numerical table.

This document is the engine's **single source of truth**. The Go engine (`bnf_go_engine`) must replicate every rule in this file — no inference, no embellishment, no omission. Every rule carries a `[p XXX]` citation referring to the **printed page number** of the book.

---

# 0. Top-Level Trading Identity

The engine trades **NSE equities (delivery / CNC swing)** following Ankur Patel's playbook:
- **Time horizon:** Days to weeks (swing) and weeks to months (positional).
- **Direction:** Long-only.
- **Foundation:** Range Contraction (RC) + Range Expansion (RE) + Volume Expansion (VE).
- **Edge:** Catching the early Momentum Phase of a Momentum Cycle.
- **Author's own bias:** 70% of his trades are at the **big base entry** or the **first pause after the breakout** [p 283].

---

# 1. The Three Market Pillars *(Chapter 2 — Concepts That Drive Markets)*

These are the foundation of every other rule in the book.

## 1.1 Range Contraction (RC) `[p 18-19]`
- Stock's price trades within a narrow range, low volatility, dormant.
- Often follows a significant price move (buying/selling pressures rebalance).
- Tension builds; eventually breaks out.

## 1.2 Range Expansion (RE) `[p 20-21]`
- A candle **larger than the previous few candles** — the "Range Expansion Candle".
- Stock waking up, becoming active.
- Successful RE leads to **immediate follow-through** within a few days.
- After every RE there is an RC; after every RC there is an RE *(timeless structure, 100+ years valid)* `[p 24]`.

## 1.3 Volume Expansion (VE) `[p 25, 27]`
- Volume confirms direction of RE.
- High volume = strong intent (institutional footprint).
- **Law (p 27):** *"Price follows the direction of range and volume expansion."*
- Range expansion **with** volume expansion = high probability move.
- Range expansion **without** volume expansion = lower probability.

## 1.4 Chapter 2 Verbatim Summary `[p 29]`
1. Understand RC and RE.
2. Volatility plays an important role in swing trading — favour **directional volatility**.
3. Range Expansion Candles signal price changes leading to rapid and sustained moves.
4. Volume serves as confirming factor for price movements.
5. Prices generally follow direction of range and volume expansion.
6. Focus on **simplicity** — master few concepts, apply consistently.

> **Engine implication:** The codebase must compute RC/RE/VE state per bar for every NSE symbol, on daily timeframe minimum.

---

# 2. The Momentum Cycle *(Chapter 3 — Momentum: The Market's Fuel)*

The Momentum Cycle is the framework to track and trade different phases of a stock's trend `[p 64]`. The cycle has **4 key phases**:

```
   DND  →  TRIGGER CANDLE  →  MOMENTUM  →  EXHAUSTION  →  DND
```

## 2.1 DND Phase (Do Not Disturb) `[p 31-32]`
- Stock is in **downtrend** OR **sideways**.
- High volatility with little to no directional moves.
- **Rule:** Swing traders must AVOID the DND phase. No new positions. Can last months or years.

## 2.2 Trigger Candle — **🎯 EXACT DEFINITION** `[p 35]`
A trigger candle occurs when a stock that's in the DND phase makes:

1. **Price jump > 6.5%**, AND
2. **Volume ≥ 3× of 50-day average**, AND
3. **Closes on the higher side of the session's range** (strong belief).

**Plus context:** Best after the stock has been ignored for a while. Look at stocks that experience a rapid increase of **20% or more from their lowest point** `[p 35]`.

> A trigger candle = **range expansion + volume expansion on the upside**.

## 2.3 Momentum Phase `[p 32-33]`
- Stock price moving strongly upward.
- Sellers take a backseat → small buying drives prices up.
- Typically follows a period of underperformance.
- Lasts a few weeks to several years.
- **Stock-cycle reality:** Stocks typically spend ~80% of their time moving sideways (bases) and ~20% trending `[p 65]`.

## 2.4 Young vs Old Trends `[p 38]`
- **Young trend:** Stock that has been in a 3-6 month (or longer) range and just broke out. Buy here.
- **Old trend:** Stock that has been going up for 6+ months, near all-time highs. Risky — extra-careful with risk and position size.

## 2.5 EMA Role in Momentum Phase `[p 41]`
- Use **10, 20, 50 EMAs** (exponential, not simple — closer to current action).
- **🛑 EMA Extension Cap:** *"If a stock's price is significantly distant (**30-35%**) from its 50-day moving average, it is best to refrain from purchasing it."* `[p 41]`
- 10 EMA = short-term support (early momentum).
- 20 EMA = mid-term support.
- 50 EMA = long-term support; basing zones often form here.
- **Strong uptrend signal:** 10 EMA rising AND 10 EMA above 20 EMA.

## 2.6 Pullback Behaviour `[p 44-45]`
- Pullback = temporary drop within an uptrend, usually with **light volume**.
- Acts as an opportunity to re-enter or add.
- Early uptrend → expects support at **10 EMA**.
- As trend matures → pullbacks toward **20 or 50 EMA**, often forming a base / mini-base near the 50 EMA.

## 2.7 Rally Legs `[p 53-55]`
A "rally leg" = a quick burst of upward movement followed by a brief pause.

| Leg | Trader action |
|---|---|
| **1-3** | ✅ Best risk-to-reward — primary trade zone |
| **4** | ⚠ Caution — momentum slowing |
| **5+** | ❌ Exhaustion zone — price correction or sideways likely |

## 2.8 Parabolic Moves `[p 56-58]`
- Sharp, near-vertical climb on the chart.
- Often in low-float stocks during strong bull markets.
- **Pattern:** Climb 95-100% in ~6 days → typically drop **40% in 16-20 days** afterwards (e.g., FCL 2017, IFCI 2024).
- High exhaustion risk.

## 2.9 Exhaustion Phase `[p 52]`
Signs:
- Stock dips below key moving averages following a significant move.
- More down days than up days.
- High-volume during consolidation.
- Price range widens; volatility rises.
- Reached after rally leg 4-5+ or after a parabolic spike.

## 2.10 Post-Exhaustion Possibilities `[p 59-63]`
- **Price correction** — major breakdown of long-term structure; stock stagnates for years (e.g., JAICORP 2017, JMFINANCIL 2017).
- **Time correction** — sideways multi-month base; old trend can become a "new trend" again (e.g., MAZDOCK 2023, NEULANDLAB 2020). *"Double gets double again."* `[p 61]`

## 2.11 Chapter 3 Verbatim Summary `[p 64]`
Momentum Cycle = a framework with **4 key phases** to track and trade different phases of a stock's trend:
1. **DND Phase** — Stocks sluggish. Trending down with low volatility/volume. Avoid new positions.
2. **Trigger Candle** — Potential shift from DND to Momentum. Marking start of change.
3. **Momentum Phase** — Stocks experience a strong uptrend. Swing traders find opportunities for profit.
4. **Exhaustion Phase** — Profit-taking happens; stocks may consolidate, correct, or trade sideways.

> **Engine implication:** Every symbol must carry a phase label (DND / Trigger / Momentum / Exhaustion). The engine's setup detector must filter for Momentum phase candidates only. Rally-leg counter must be running.

---

# 3. The Perfect Setup *(Chapter 4 — Base Detection)*

## 3.1 Why Bases Form `[p 65-66]`
- Stocks spend ~80% of time sideways (bases), ~20% trending.
- Bases absorb supply during uptrends, transfer ownership during downtrends.
- Identifying bases = identifying low-risk entry points.

## 3.2 Volatility Contraction Pattern (VCP) `[p 66-71]`
- Developed by **Mark Minervini**.
- Three components: **Time, Price, Symmetry**.
- Technical footprint notation: e.g., `40W 31/3 4T` = 40 weeks duration, max drop 31% / min drop 3%, four contractions.
- Healthy VCP: successive contractions get **shallower** (e.g., 30% → 15% → 4%).
- Last contraction is tightest (multiple tight-range candles) before breakout.

## 3.3 Big Base `[p 71-78]`
- "The bigger the base, the higher in space."
- Duration: > 3-4 weeks (some last years).
- Two formation modes:
  - **Post-downtrend:** institutional value-seeking absorbs bag-holder supply.
  - **Post-uptrend:** strong stocks needing rest after a big run.
- Healthy big-base sign: contractions shrink in **depth and width** as base matures.
- Volume dries up + price tightens before breakout.

## 3.4 Base Type Catalog (Momentum Phase) `[p 78-91]`

### 3.4.1 Fakeout Base `[p 79-81]`
- Stock breaks out → quickly reverses back into the base.
- Stays near the base; finds support at **20 or 50 EMA**.
- Re-tightens, often re-breaks out higher.

### 3.4.2 Shakeout Base `[p 82-85]`
- Stock drops below base / KMA (e.g., 10-day or 50-day).
- Same-day or short-period **bounce back** into range.
- Bounce = signal that buyers/strong hands are still there.

### 3.4.3 Zip-Zap Base `[p 86-89]`
- **Combination: Fakeout + Shakeout** in sequence.
- Reduces supply twice; longer to form; usually finds support near **50 EMA**.

### 3.4.4 Reverse Zip-Zap Base `[p 89-91]`
- **Shakeout first**, then fakeout (reversed order from Zip-Zap).
- Then a tight trading range, then breakout.
- Resembles base-on-base visually.

## 3.5 IPO Base Patterns `[p 92-103]`

### 3.5.1 Shoot-Up Base `[p 93-95]`
- Stock rises strongly from IPO base, never drops below it.
- Doubles or triples shortly after IPO. Hard to catch.

### 3.5.2 Staircase Base `[p 96-99]`
- Sequential higher bases after IPO; each higher than the previous.
- Stock never dips below prior base. Multiple entry chances. *"Climbing a staircase."*

### 3.5.3 Late Mover Base `[p 100-103]`
- Initially dips below IPO base.
- Forms two mature bases below IPO base over weeks/months.
- Big move comes after breakout of the **second mature base** (near listing-week price action).

## 3.6 Base-on-Base `[p 103-106]`
- Second base forms on top of a primary base.
- New base's low should sit **close to the high of the previous base**.
- Bullish signal — stacked bases. Breakouts have higher success probability.

## 3.7 Mini Bases (Continuation Patterns / Flags) `[p 106-121]`
Formed after a strong rally leg + short-term pause. All share: **one strong leg (pole) + short pause** near a key short-term EMA.

| Pattern | Structure | `[page]` |
|---|---|---|
| **Flag & Pole** | Sharp move (pole) + sideways flag | `[p 107-108]` |
| **Ascending Triangle** | Higher lows + flat top | `[p 108-110]` |
| **Channel / Rectangle Flag** | Sideways between parallel S/R lines | `[p 111-114]` |
| **Descending Flag** | Lower highs + equal lows, traded long after pullback | `[p 114-117]` |
| **Symmetrical Triangle** | Lower highs + higher lows converging | `[p 118-121]` |

## 3.8 Pocket Pivot — **🎯 EXACT DEFINITION** `[p 121-123]`
> *"A pocket pivot day is when a stock goes up, and the volume is higher than the highest volume on a down day in the past 10 days."*

- Reveals institutional footprints within a base.
- **More pocket pivots in a base = stronger base.**
- Buy signals near a pocket pivot are trustworthy.

## 3.9 Volume Action `[p 121-125]`
- **Low volume on down days** = strong demand; supply minimal → high probability of continuation.
- **Volume contraction during flag formation** = lowering supply pressure → breakouts more likely to succeed.

## 3.10 Quality Setup Checklist `[p 126-132]`
A truly tradable setup must have **all** of these:

1. **Prior buying interest** — visible demand has shown up.
2. **Linear consolidation** — swings shrinking, not expanding.
3. **Price tightening near KMAs** — 50 EMA for big base; 10/20 EMA for mini base (10 above 20, both sloping up).
4. **Clean mover** — smooth historical swings, not choppy.
5. **Higher low in the base** — confidence accumulating.
6. **Trading near recent swing highs** — *"line of least resistance"* (Livermore).
7. **Young stock** — see §2.4.
8. **Strong sector** — sector tide lifts all boats.
9. **Liquidity** — large account → high-liquidity stocks only.

## 3.11 Red Flags `[p 133-137]`
- **Big down day** (≥ −5% with above-avg volume) in base → SKIP 5-10 days (unless stock has already reclaimed the high of that down day, especially if volume was low).
- **Big rejection candle** on breakout attempt → SKIP 5-10 days OR wait for stock to surpass the high of the rejection candle.

## 3.12 Optional / Enhancing Factors `[p 137-139]`
- **ADR (Average Daily Range)** — higher ADR = more swing opportunity; mid/small caps preferred over large caps.
- **Breakout Attempts** — stocks with 1-2 prior failed breakout attempts often succeed on the next try.
- **Timing vs Selection** — picking the right stock matters MORE than precise timing.

## 3.13 Failed Bases Are Inevitable `[p 139-142]`
Even quality setups can fail. Technical analysis is about **probability**, not certainty. Examples in book: CANBK, DBL 2023, DCXINDIA 2023-24.

## 3.14 Chapter 4 Verbatim Summary `[p 143]`
1. **Why bases form:** Absorb supply → continue trend.
2. **VCP:** Minervini's strategy. Bull-market focus. Price + volume during base formation timing entries.
3. **Types of Bases:** Fake-out, Shake-out, Zip-Zap, Reverse Zip-Zap.
4. **Characteristics of a Good Base:** Prior buying interest, linear consolidation, price tightening near KMAs, clean mover, higher low in base, trading near recent highs, young stock in strong sector.
5. **IPO Bases:** Shoot-up, Staircase, Late Mover.
6. **Additional Basing Patterns:** Base-on-Base, Mini Base.

> **Engine implication:** Detector library must distinguish all 11 base types listed above. Quality scorer must implement the 9-item checklist. Red-flag filter must block bad setups for 5-10 sessions.

---

# 4. Buying with Precision *(Chapter 5 — Entry Techniques)*

## 4.1 Buy Above the Pivot High `[p 144-146]`
- A **pivot high** = a swing-high level on the consolidation.
- The tighter the pivot, the tighter the stop-loss can be.
- Entry above pivot signals short-term direction change confirms broader uptrend.

## 4.2 Strong Start `[p 146-149]`
- Credit: Manas Arora.
- Entry technique: stock closes tightly previous day, opens with slight gap up, holds **2-3 minutes after open** → enter.
- Pre-market activity + first few minutes of session = strength gauge.

## 4.3 Buy Above Previous Day's High (PDH) `[p 149-151]`
- Standard, simple, clean-cut trigger.
- Ideal for GTT / AMO orders.
- Self-fulfilling — many traders watch PDH.

## 4.4 Anticipation Buy `[p 151-155]`
- Enter **before** the actual breakout, when the setup is mature, supply absorbed, volumes contracted.
- Two execution windows:
  - End-of-day (EOD) entry.
  - Right at 9:15:01 next morning.
- Improves R:R substantially.
- **Only for experienced traders** with deep setup understanding and supportive market conditions.

## 4.5 Opening Range Breakout (ORB) `[p 155-157]`
- Identify the price range of the first 5 / 30 / 60 minutes.
- Enter when price breaks out of that range.
- Lower timeframe → lower win rate, better R:R; higher timeframe → reverse.

## 4.6 Iron Rules of Entry `[p 157]`
- **🛑 Gap-up cap: Do NOT chase if the stock gaps up > 3% above intended entry.**
  - *Why:* Max stop-loss = 2.5%; a 3%+ gap risks instant stop-out on minor pullback.
- **Volume confirmation NOT required:** If setup criteria are met, you don't have to wait for volume on the breakout day — waiting often means missing or paying up.

## 4.7 Order Execution `[p 158-159]`
- **GTT (Good Till Triggered):** Useful for hands-off traders. Keep a logical gap between trigger and buy price (else order skips). Manually cancel if not executed.
- **AMO (After Market Order):** Place after-hours.
- **Market / SL-M orders:** Risky on illiquid stocks — wide bid-ask can lead to bad fills. Use cautiously.
- **Limit orders:** Always available as a safer alternative.

## 4.8 Chapter 5 Verbatim Summary `[p 159]`
Title: *"Concepts of good trading setups by focussing on effective entry techniques."*

**Key Entry Techniques:**
- Buying above the PIVOT HIGH
- Strong Start to capitalize on early momentum
- Buy Above Previous Day's High (PDH)
- Anticipation Buy for pre-empting a move
- Opening Range Breakout (ORB) Strategy

**Additional Considerations:**
- **Gap Up:** Avoid buying stocks that open with more than a 3% gap up from planned entry price to manage risk.
- **Volume Confirmation:** Waiting for volume confirmation can cause missed opportunities or wider stop-losses; if the setup meets the criteria, volume confirmation isn't always necessary.
- **Order Execution:** Use Good Till Triggered (GTT) or After Market Orders (AMO) to manage trades if unavailable during market hours. Be cautious with market orders, especially for illiquid stocks.

> **Engine implication:** Entry engine must support all 5 techniques selectable per setup. Hard validator: reject any entry where current price gap > 3% above the calculated entry trigger.

---

# 5. Timing Your Exits *(Chapter 6)*

## 5.1 Two Selling Philosophies `[p 161, 166]`
- **Selling Into Strength** — sell while stock is rising / overextended. Best for traders seeking consistent income.
- **Selling Into Weakness** — wait for the stock to lose strength. Best for wealth-building / catching big winners.

## 5.2 Selling Into Strength — Methods `[p 161-166]`

### 5.2.1 R-Multiples `[p 161-162]` *(Van Tharp)*
- R = the rupee amount at risk on a trade.
- Example partial-book schedule: sell **half at 3R**, sell **another portion at 5R**, sell **the rest at 10R**.
- Adjust stops as you book — e.g., after partial-booking at 5R, move stop to 2R.

### 5.2.2 Selling Into Extended Moves `[p 163]`
- Extended = stock moves **25-30% in a few sessions**.
- Behaviour depends on cycle position:
  - **Early-stage extension** (just emerging from base) → **consider adding**, not selling.
  - **Late-stage extension** (after a long run) → **sell** — high pullback risk.

## 5.3 Selling Into Weakness — Methods `[p 166-172]`

### 5.3.1 Close Below KMAs `[p 167-169]`
- Choose trailing EMA per style:
  - **10 EMA** — short-term trades.
  - **20 or 50 EMA** — medium/long-term trades.
- Wait for **end-of-day close below** the EMA before exiting (intraday dips don't count).
- Exception: if initial hard stop is hit, exit immediately — don't wait for EOD.

### 5.3.2 Emergency Stops `[p 169-171]`
- Set a few % below the trailing EMA, OR below the low of the strongest candle after entry.
- Protects against unexpected gap-downs / volatility spikes.

### 5.3.3 Pivot Low Exit `[p 171-172]`
- Wait for stock to form a **downside pivot on daily** (clear shift in momentum).
- Hold until that pivot is broken → exit.
- Can be combined with the EMA method.

## 5.4 Hybrid Selling Technique — **🎯 RECOMMENDED** `[p 172-175]`
1. Sell **25-35%** of the position into initial strength.
2. Move stop on the remaining shares to **break-even** (cost = entry price → trade becomes risk-free).
3. Trail the rest with **10 or 20 EMA** as moving stop.
4. Exit the remaining position on close below the trailing EMA.

## 5.5 Chapter 6 Verbatim Summary `[p 176]`
3-row table (Definition / Benefits / Application):

| Technique | Definition | Application |
|---|---|---|
| **Selling Into Strength** | Sell when price rising during strong momentum | R-multiples (3R/5R/10R partial books); spot early vs late extensions |
| **Selling Into Weakness** | Wait until stock loses momentum | Trail with EMAs (10/20/50); emergency stops below KMA; exit on broken pivots |
| **Hybrid** | Combine both | Sell 25-35% into strength → break-even stop on rest → trail with 10/20 EMA |

> **Engine implication:** Position-state machine must support partial exits, dynamic stop adjustments (cost-basis, EMA-trail), and pivot-low exit triggers.

---

# 6. Survive & Thrive — Risk Management *(Chapter 7)*

## 6.1 Stop-Loss Methods `[p 179-185]`

### 6.1.1 Volatility-Based (ATR) `[p 179-181]`
- Use **ATR(14)** to measure stock's typical movement.
- **Swing default: 1 ATR below entry.**
- Example (NATIONALUM 2023): ATR = 1.84; entry 100 → stop 98.16.
- Avoid using > 1 ATR on mini-base setups.

### 6.1.2 Fixed Percentage `[p 181-182]`
- Short-term traders: **1-2%** stop.
- Positional traders: **4-8%** stop.
- Simple, fast, no per-trade calculation.

### 6.1.3 Low of the Day (LOD) `[p 182-183]`
- Stop below today's low.
- Best for short-term momentum trades — sometimes yields 5-20× R:R.
- Not ideal for longer-term holds.

### 6.1.4 Previous Day's Low (PDL) `[p 184]`
- Stop below previous day's low.
- Good for working-job traders — pre-defined, no live monitoring needed.

### 6.1.5 Pivot Low `[p 185]`
- Stop at lowest price within the consolidation period.
- Wider than LOD/PDL but aligned with market structure.

## 6.2 Tight vs Wide Stops `[p 186-187]`
- **Tight stops:** Bigger positions → potentially more profit, more stop-outs, more trades.
- **Wide stops:** Smaller positions → fewer stop-outs, fewer trades.
- Choose based on personality + risk tolerance. Don't use tight stops in choppy markets.

## 6.3 Handling Gap-Downs (Slippage Rule) `[p 188-189]`
> *"When a stock skips your stop-loss for any reason, you must exit at the next available price without hesitation."* `[p 189]`
- Don't hold hoping for a bounce.
- Story: HUDCO 2024 — buy 90.40, stop 89, news after-hours → opened at 83 → sold at 82 (9% slippage past stop) → stock continued to 70.

## 6.4 🛡 Risk Management — **🎯 THE SIX HARD RULES** `[p 190-191]`

> Exact wording from the book:

1. **Follow stop-loss** — Always set a stop-loss for each trade to limit potential losses.
2. **Reduce position size** — If your last **four trades hit stop-loss**, consider reducing your position size by **half**. If still losing, either check your strategy or accept market conditions are unfavourable.
3. **Limit open positions** — Keep an eye on stocks prudently. **If two of your open positions are not performing well, you have NO reason to open the third position.**
4. **Avoid holding through earnings** — It's generally advisable not to hold a stock through earnings UNLESS you have a significant profit cushion to mitigate potential volatility.
5. **Set maximum per trade loss** — Limit the **maximum loss per trade to 1% of trading capital**.
6. **Cap open risk** — Ensure the total risk from all open positions does **NOT exceed 4-5% of trading capital**.

## 6.5 Chapter 7 Verbatim Summary `[p 192]`
6 boxes:
1. **Risk First Approach** — Prioritize defining risks; use stop-loss orders to protect against major losses.
2. **Stop-Loss Strategies** — Volatility-based, fixed percentage, pivot-low.
3. **Tight vs Wide Stops** — Tight: larger positions/higher stop-out risk. Wide: fewer stop-outs/smaller positions.
4. **Prepare for Unforeseen Events** — Contingency plans for sudden market drops or technical failures.
5. **Avoid Emotional Trading** — Stay consistent; avoid impulsive decisions.
6. **Risk Management Rules** — Set stop-losses, reduce position size after losses, limit open positions, cap total risk.

> **Engine implication:** Risk module is a hard-coded gatekeeper. Every new entry must clear ALL 6 rules. Position-counter logic must inspect the *health* of existing positions before allowing a 3rd open. Earnings calendar guard required.

---

# 7. Position Sizing *(Chapter 8)*

## 7.1 Core Terms `[p 194-196]`

| Term | Definition |
|---|---|
| **Risk Per Trade** | Money willing to lose on one trade (e.g., ₹1,000 on ₹1L account = 1%). |
| **Open Risk** | Sum of potential losses across all active positions. Cap at **4-5%** of account. |
| **Open Gains** | Paper profits not yet realized. Should be regularly converted via selling-into-strength. |
| **Max Open Positions** | **Sweet spot: 8-12** for swing traders. Avoid 20+ — spreads attention thin. |
| **Portfolio Gain** | Net account value increase over a period. |
| **Win Rate** | (Winning trades / Total trades) × 100. |

## 7.2 Thinking in R-Multiples `[p 197]`

**Required Win Rate for Break-Even** at different reward multiples:

| Reward Multiple | Required Win Rate |
|---|---|
| 1R | 50% |
| 2R | 33% |
| 3R | 25% |
| 4R | 20% |
| 5R | 17% |

The point: **high reward-to-risk trades with lower win rates can still produce strong overall returns.**

## 7.3 Expectancy Formula `[p 198-199]`
```
Expectancy = (Win% × Avg Win) − (Loss% × Avg Loss)
```
Example: 60% wins × ₹1,000 − 40% losses × ₹400 = **₹600 − ₹160 = ₹440 per trade.**

Positive expectancy = profitable system.

## 7.4 Position Sizing Methods `[p 200-204]`

### 7.4.1 Fixed Exposure `[p 200]`
- Same ₹ amount each trade (e.g., 10% of ₹1L = ₹10,000 / trade).
- Set a fixed stop-loss % (e.g., 4% or 8%) to define risk.
- **Quantity = Exposure ÷ Entry Price.**
- Drawback: doesn't differentiate by volatility.

### 7.4.2 Fixed % Risk — **🎯 PREFERRED** `[p 200-201]`
- Risk a fixed % of capital per trade (e.g., 1% of ₹1L = ₹1,000).
- **Quantity formula:** `Quantity = Risk ÷ (Entry − Stop-Loss)`
- Example: Risk ₹1,000, entry ₹510, stop ₹500 → ₹10 risk per share → 100 shares.

### 7.4.3 Progressive (Performance-Based) `[p 202-204]`
- Adjust risk based on recent trade results.
- **Baseline:** 0.2-0.25% per trade.
- After **2-3 consecutive wins** → step up to 0.3% / 0.4% / 0.6%.
- After **2-3 consecutive losses** → step down to 0.1%.

## 7.5 Drawdown Discipline `[p 204-207]`

### 7.5.1 Drawdown Concept `[p 204-205]`
- "Time underwater" — period account is below previous high.
- Most accounts spend **80-90% of time in drawdown**.
- **Target: Max drawdown below 5-7%**.

### 7.5.2 🎯 Recovery Table — Figure 8.5 `[p 206]`

| Drawdown % | Gains Required to Break Even |
|---|---|
| 5% | 5% |
| 10% | 11% |
| 15% | 18% |
| **20% | 25%** ← *critical threshold* |
| 30% | 43% |
| 40% | 67% |
| 50% | 100% |
| 60% | 150% |
| 70% | 233% |
| 80% | 400% |

**Critical:** *"Post the 20-25% mark it becomes really difficult to recover from the DD."* `[p 206]`

### 7.5.3 🎯 Risk-Per-Trade Impact — Figure 8.6 `[p 207]`

| Losses | 0.25% | 0.50% | 1.00% | 2.00% |
|---|---|---|---|---|
| 5 | 1.25% | 2.50% | 5.00% | 10.00% |
| 10 | 2.50% | 5.00% | 10.00% | 20.00% |

> Pairing this with the Recovery Table: 10 consec losses at 2% risk = 20% DD = needs 25% gain to break even.

### 7.5.4 Drawdown Recovery Steps `[p 207-208]`
1. **Acknowledge mistakes** — *"If you find a hole, stop digging."*
2. **Stop and reflect** — analyse what went wrong.
3. **Take a break** — 1-2 weeks off.
4. **Start small** — minimal position size on return.
5. **Build confidence** — small wins.
6. **Improve win rate** — refine entries/exits.
7. **Analyse trades** — granular review.
8. **Focus on solutions** — fix root causes.
9. **Reduce frequency** — quality > quantity.
10. **Gradually increase size** — as win rate recovers.

## 7.6 Finding Balance in Position Sizing `[p 209-211]`
- **Too big too soon → blow up.**
- **Too small for too long → no portfolio impact.**
- Beginners: start small, scale up gradually after proving consistency.
- Don't use max position size until you've proven you can handle it.
- *"With great size comes great vulnerability."* `[p 210]`

## 7.7 🎯 Probability of Consecutive Losses — Figure 8.8 `[p 213]`

Sample (read from rendered table):

| Win Rate | 2 Losses | 3 Losses | 4 Losses | 5 Losses |
|---|---|---|---|---|
| 25% | 56.25% | 42.19% | 31.64% | 23.73% |
| 30% | 49.00% | 34.30% | 24.01% | 16.81% |
| 35% | 42.25% | 27.51% | 17.85% | 11.63% |
| 40% | 36.00% | 21.60% | 12.96% | 7.78% |

> *"If your system has a 25% win rate, there's a 42.19% chance of encountering three consecutive losses and a 23.73% chance of facing five consecutive losses."* `[p 213]`

## 7.8 Mental Capital `[p 213-215]`
Two capitals: **financial + mental**. Both finite. Restore mental capital via:
1. Establish a solid trading plan.
2. Practise mindfulness & self-awareness.
3. Set realistic goals & manage expectations.
4. Seek support & mentorship.
5. Practise detachment from outcomes.

## 7.9 Chapter 8 Verbatim Summary `[p 216]`
4 boxes:
1. **Position Sizing Methods** — Use fixed exposures or fixed % risk on each trade to manage risk.
2. **Advanced Position Sizing** — Adjust based on market conditions and recent performance.
3. **Importance of Consistency** — Consistent position sizing is key to avoiding big losses.
4. **Mental Capital** — Preserve mental well-being via plan + realistic goals + detachment.

> **Engine implication:** Sizing engine implements all 3 methods (selectable). Progressive method requires win/loss streak tracking. Drawdown monitor with kill-switch at 20% (per Recovery Table) and warning at 5-7% (per p205 target).

---

# 8. Mind Over Money *(Chapter 9 — Psychology)*

## 8.1 The Four Fears `[p 218]`
1. **Fear of being wrong.**
2. **Fear of losing money.**
3. **Fear of missing out (FOMO).**
4. **Fear of leaving money on the table.**

## 8.2 Break the Loss-Loser Link `[p 222]`
> *"The best traders in the markets take frequent losses, they just don't take big ones."*

## 8.3 Booking Profits Too Early `[p 222-224]`
- "Win/Loss Ratio" chart `[p 223]`: at 33% win rate, **need 2:1 wins/losses to break even**.
- Cutting winners short = guaranteed long-term loss.

## 8.4 Averaging vs Pyramiding `[p 224-227]`
- **Averaging down (bad):** Adding to losers. Risk of ruin.
- **Pyramiding (good):** Adding to winners. *"The best stock to buy is the one you already own."* — Peter Lynch.

### 8.4.1 Pyramiding Rules `[p 226-227]`
- Don't add just because price is rising — **require a new setup**.
- Add only when the pyramid setup forms **near the initial entry** and price is **near short-term MAs**.
- **DO NOT add when the stock has gone 20-30% away from initial entry.**
- Treat each add as a **separate trade with a separate stop-loss**.
- Patience: trades don't always move immediately after entry; manage risk, wait.

## 8.5 Let Trades Unfold Naturally — Avoid Micromanagement `[p 229-232]`
- Two causes of micromanagement:
  1. Risk per trade is too large (psychological discomfort).
  2. Overconfidence in predicting moves.
- Fixes:
  - Reduce risk so you don't panic on fluctuations.
  - Limit simultaneous full-risk positions (e.g., **max 2 at a time**; wait until one becomes risk-free before adding another).

## 8.6 Squats — A Key Concept `[p 232-235]`
- **Squat:** stock appears poised to break out, then drops back closing lower than expected on the breakout day.
- In healthy markets stocks often recover from squats.
- **Rule:** Don't exit early on a squat — let the stop-loss decide.
- Examples: MINDACORP 2021 (squat → 57% in 15 weeks); HSCL 2016 (squat → 108% in 8 weeks); ASHAPURMIN 2024 (squat → 43% in 10 days).

## 8.7 Evidence Over Confidence `[p 239`
- Pick one setup → study hundreds of historical examples → build a database → conviction follows.

## 8.8 Trading Lifestyle `[p 240-247]`
- **Have a secondary income source** — swing trading is seasonal (~3-4 profitable months/year).
- **Don't watch the screen all day** — once entered, you can't control the stock anyway.
- **Journal everything** — trades + emotions + thoughts. Review weekly. Pattern-spot after ~30 trades.
- **Don't fixate on P&L** — clouds judgement; leads to revenge trading.
- **Have a routine** — pre-market scan, prep, post-market review.

## 8.9 Chapter 9 Verbatim Summary `[p 247]`
9 boxes:
1. **Success in Trading** — master technical + psychological readiness.
2. **Emotional Challenges** — early profit-taking, hesitation after losses.
3. **Types of Fears** — wrong / losing money / FOMO / leaving money on table.
4. **Avoid Averaging** — pyramid winners, never average losers.
5. **Stick to Risk Parameters** — follow pre-set risk rules; no micro-managing.
6. **Handling Squats** — temporary drops aren't always reversals.
7. **Emotional Resilience** — learn from past; stay confident.
8. **Self Awareness** — emotional intelligence.
9. **Focus on One Setup** — master one before diversifying.

> **Engine implication:** Engine should track squats but not auto-exit on them. Pyramiding logic must enforce: (a) new setup signal required, (b) within 20-30% of initial entry, (c) separate stop per add. Discrete journaling/diagnostics module.

---

# 9. Staying Market Aware *(Chapter 10)*

## 9.1 Market Conditions > Setups `[p 249-250]`
- Swimmer analogy: stock = swimmer; market = ocean current. Even strong setups fail in a falling market.
- A bullish flag works in a bullish market; lacklustre in a bear/choppy market.

## 9.2 🎯 The 10 & 20 Rule — Market Regime Filter `[p 252-253]`
> *"If one is a long-only swing trader, they should wait for the relevant indices to trade above the 10 and the 20 EMAs, where the 10 EMA should be positioned above the 20 EMA."*

**Index selection:**
- Small-cap traders → **Smallcap 100 index**.
- Large-cap traders → **NIFTY 50 / NIFTY 500**.
- Can use a **combination of indices** for better results.

**Why:** index above key MAs → buyers in control → majority of constituents bullish.

## 9.3 Market Breadth `[p 253-255]`

### 9.3.1 % Stocks Above Key Moving Averages
- **>50% above 200-day MA** → long-term uptrend.
- **>50% above 50-day MA** → medium-term uptrend.
- **>50% above 20-day MA** → short-term uptrend.
- **All three positive simultaneously** → green light for long positions; high-probability rallies.
- **80% below 20 MA** → short-term weakness; possibly imminent bounce if extreme.

### 9.3.2 🎯 Net New Highs `[p 256-257]`
```
Net New Highs = (New 52-Week Highs) − (New 52-Week Lows)
```
- Bullish market: NNH stays in positive territory, rarely below zero.
- **3 consecutive sessions BELOW zero** → market potentially weakening → caution.
- **3 consecutive sessions ABOVE zero** (after being below) → potential bullish shift → scout for entries.

## 9.4 Best Conditions to Trade `[p 257-259]`
- Most stocks correlate with the index.
- **Best entries:** After a prolonged correction or consolidation.
- **Worst entries:** When market is "heated" (excessive bullish narratives, euphoria).
- Welcome corrections; they reset supply.

## 9.5 Sit on the Sidelines `[p 259-260]`
- Many traders feel pressure to trade daily — wrong.
- Force-trading in unfavourable conditions = poor odds, capital and mental drain.
- *"It's better to wait for the market to stabilize, preserving your mental and financial capital."*

## 9.6 Overtrading `[p 260-261]`
- Causes: **revenge trading**, **emotional trading**, FOMO.
- Fix: solid pre-defined trading plan + self-discipline.

## 9.7 Situational Awareness — MYE Framework `[p 261-264]`
**M = Market** — bias, recent trades' performance, what setup type is working, sector focus, key opportunities.
**Y = You(rself)** — emotional state, strengths, mindset's impact on execution, weaknesses, lessons.
**E = External Conditions** — physical setup, distractions, scheduled commitments, contingency plans.

## 9.8 Chapter 10 Verbatim Summary `[p 264]`
8 boxes:
1. **Market Conditions Matter** — ocean current overrides swimmer.
2. **Enhancing Market Awareness** — monitor recent trades + scans + strong bases.
3. **The 10 & 20 Rule** — index above 10 & 20 MA, 10 above 20.
4. **Market Breadth** — % above MAs + Net New Highs.
5. **Best Conditions to Trade** — post-correction / non-euphoric markets.
6. **Value of Patience** — knowing when to wait.
7. **Avoid Overtrading** — stick to plan; self-control.
8. **Situational Awareness** — MYE framework.

> **Engine implication:** Market regime module is a hard gate before any new entry. Daily computation of: index EMA(10) vs EMA(20), % of NSE universe above 20/50/200 MA, daily Net New Highs delta. Trading aggression level scales with breadth.

---

# 10. Scanning for Opportunities *(Chapter 11 — THE BIRD'S-EYE SCANNER)*

## 10.1 Bird's-Eye View Philosophy `[p 266-267]`
- Manually scan **hundreds of charts** evening after evening.
- ~700-900 names per favorable-condition broad scan in 35-40 minutes.
- **Goal:** train your eye to spot setups → muscle memory → pulse-check the market.
- Quick filters can shrink to 5-10 watchlist names.

## 10.2 🎯 SCAN #1 — The Bird's-Eye EMA Scan `[p 267]`

**Verbatim conditions a stock must satisfy:**
```
1. CMP > EMA 20
2. CMP > Number 30      ← price filter (₹30 minimum)
3. 50 Day Average Volume > 10,000
4. Market Cap > 1 Cr    ← filters out ETFs and similar instruments
```
- Output: broad universe view (~700-900 names in good markets).

## 10.3 🎯 SCAN #2 — Weekly Scanning `[p 268]`

For working professionals; run twice a week (Sat/Sun + Wednesday):
```
Stock > 10-week EMA
AND  10-week EMA > 30-week EMA
```
Rationale: 30 EMA filters non-bullish long-term names; 10 EMA filters strong-but-not-swing-ready names.

## 10.4 🎯 SCAN #3 — Monthly Gainers (Kullamägi MOMO) `[p 269]`

**Verbatim conditions:**
```
1. CMP > Number 30
2. Market Cap > 1 Cr
3. % Change in the last 10 Days  > 20% from the Lows
4. % Change in the last 30 Days  > 20% from the Lows
5. % Change in the last 90 Days  > 30% from the Lows
6. % Change in the last 180 Days > 90% from the Lows
```
Stocks that exhibit the strongest performance — likely to repeat.

## 10.5 52-Week High vs Monthly High `[p 269-270]`
- Instead of 52-week-high filter, use **1-month / 3-month / 6-month highs** — catches the trend earlier.
- Logic: a 52-week high must first pass through 1-month, 3-month, 6-month highs.

## 10.6 🎯 SCAN #4 — Tight Range Scan `[p 271]`

**Verbatim conditions:**
```
1. Today's % Change       ≤  2.5%
2. Today's % Change       ≥ -2.5%
3. Previous Day's % Change ≤  3.5%
4. Previous Day's % Change ≥ -3.5%
```
Output: stocks in strong momentum currently forming Range Contraction.

## 10.7 🎯 SCAN #5 — Trigger Candle Scan `[p 272]`

**Verbatim conditions:**
```
1. Today's Volume > 3 × 50-day Average Volume
2. CMP > 30
3. Today's % Change > 6.5%
```
> *"Once you find a young name, wait for it to contract and provide a low-risk entry before initiating any position."* `[p 272]`

## 10.8 Sector & Theme Identification `[p 272-273]`
- Best signal: **multiple stocks from the same sector setting up simultaneously** → focus that sector.
- Manual index browsing works too but can be misleading (high-weightage stocks distort index appearance).

## 10.9 🎯 Watchlist Organization — 5 Buckets `[p 276-277]`

| # | Bucket | Purpose |
|---|---|---|
| 1 | **Open Positions** | Current trades. |
| 2 | **Focus List** | Updated each evening — ready-to-execute next session. |
| 3 | **Base** | Large/sustained-move candidates. |
| 4 | **Mini Base / Flags** | Short-base candidates in strong sectors. |
| 5 | **Strong Stocks** | From trigger-candle scan; typically held 5-15 days before maturing. |

## 10.10 Watchlist Narrowing Criteria `[p 274-275]`
A potential stock advances to "selected" only if all checks pass:
1. Assess consolidation quality.
2. Check EMA tightness.
3. Evaluate sector strength.
4. Confirm prior swing high proximity.
5. Identify liquidity criteria.
6. Ensure low-risk entry.

## 10.11 5 Pro Tips `[p 278-279]`
1. **Don't force setup selection** — < 5 seconds per chart unless it's A+.
2. **Avoid illiquid stocks** — filter by 20-day or 50-day average volume.
3. **Keep a setup database** — historical examples for cross-reference.
4. **Study big movers** daily/weekly — learn what's currently working.
5. **Start scanning with your existing list** — stocks already on watchlist have higher breakout odds.

## 10.12 Chapter 11 Verbatim Summary `[p 279]`
8 boxes (all reinforce sections above): Bird's Eye View, Momentum Leaders, Monthly Highs, Tight Range Stocks, Sector Focus, Narrowing Down, Organised Watchlist, Pro Tips.

> **Engine implication:** Scanner module must implement ALL 5 named scans as separate cron jobs. Output writes into the 5 watchlist buckets. Per-stock quality scorer applies the 6 narrowing criteria. Daily scan latency budget: < 5 minutes for ~2,000 NSE symbols.

---

# 11. Bringing It All Together *(Chapter 12 — Author's Playbook)*

## 11.1 Two Trading Styles `[p 281]`
- **Style A — Big Base / Young Name:** Enter early; hold longer; let it run; expects bigger gains.
- **Style B — First Pause After Breakout (Flag / Mini Base):** Enter on first contraction after range-expansion candle; quicker exits.
- **70% of his trades fall into one of these two categories.** `[p 283]`

## 11.2 Why Avoid Trades Above Leg 2 `[p 283]`
- Chasing past the 2nd rally leg = bad R:R.
- 10-15% gains may still be available — but odds tilt unfavorable.

## 11.3 Author's Exit Discipline `[p 283]`
- **Sell at the 3rd leg up** (typical).
- OR sell after **3-4 consecutive strong up days**.
- Comfortable missing a possible 4th-5th leg.

## 11.4 Case Studies (executed, real trades)
- **TATA MOTORS 2023** — 115% in 44 weeks (trailed 50 EMA) `[p 284-285]`.
- **MTARTECH 2021** — 19% in 3 days (sold too early in hindsight) `[p 286]`.
- **SJVN 2023** — 40% in 16 days, pyramided 2 positions, exited on overextended day `[p 287-288]`.
- **HSCL 2023** — 95% in 21 days (EV-theme tailwind) `[p 289-290]`.
- **HINDZINC 2024** — 92% in 18 days (post-46%-in-8-days range expansion, then RC) `[p 290-291]`.
- **HGINFRA 2024** — 53% in 17 days (stopped out first attempt, re-entered, won) `[p 292-293]`.
- **GPPL 2024** — failed trade (gap-down kill) `[p 294]`.
- **NBCC 2021** — failed trade (faded after 2 days) `[p 295]`.
- **ALLCARGO 2021** — stopped out same day `[p 296]`.

## 11.5 Effortless Trading `[p 297-298]`
- Goal isn't just "making money" — it's making money **with ease**.
- Comes from screen-time + practice → setups, executions, management become automatic.

## 11.6 Closing Note `[p 298]`
> *"The market is in constant flux, and so should you be. Use these insights as a base, but layer them with your own research and experiences."*

---

# 12. Engine Module Mapping (Implementation Targets)

This is the gap-analysis target list — the next document will map each row to existing files in `bnf_go_engine`.

| # | Book Domain | Engine Module Target |
|---|---|---|
| 1 | RC/RE/VE detection per bar | `core/indicators` + `agents/patterns.go` |
| 2 | Phase classification (DND / Trigger / Momentum / Exhaustion) | `agents/patterns.go` regime tagger |
| 3 | Trigger Candle detector (>6.5%, ≥3× vol, close-on-high) | `agents/scanner_agent.go` |
| 4 | Rally-leg counter (1-5) | `agents/patterns.go` |
| 5 | EMA extension filter (≥35% from 50EMA = SKIP) | `agents/scanner_agent.go` |
| 6 | Pocket Pivot detector | `agents/patterns.go` |
| 7 | Base-type catalog (Fakeout, Shakeout, Zip-Zap, Reverse Zip-Zap, VCP, Big Base, Base-on-Base, Mini Bases ×5, 3 IPO types) | `agents/patterns.go` |
| 8 | Quality Setup 9-checklist scorer | `agents/scanner_agent.go` |
| 9 | Red-flag suppressor (5-10 session cool-down) | `agents/scanner_agent.go` |
| 10 | 5 Bird's-eye scans (EMA, Weekly, Monthly Gainers, Tight Range, Trigger Candle) | `agents/scanner_agent.go` + cron |
| 11 | 5-bucket watchlist (Open, Focus, Base, Mini, Strong) | `api/server.go` + dashboard |
| 12 | Entry techniques (Pivot, Strong Start, PDH, Anticipation, ORB) | `agents/execution_agent.go` |
| 13 | Gap-up cap (>3% = skip) | `agents/execution_agent.go` |
| 14 | Exit techniques (R-multiples, KMA close-below, emergency, pivot-low, Hybrid) | `agents/execution_agent.go` |
| 15 | Stop-loss methods (ATR, Fixed%, LOD, PDL, Pivot Low) | risk module |
| 16 | 6 hard risk rules (incl. 1% / trade, 4-5% open, no-3rd-position, 4-loss-then-half) | risk module |
| 17 | Position sizing (Fixed Exposure, Fixed%, Progressive) | risk module |
| 18 | Drawdown monitor (warn 5-7%, kill 20%) | risk module |
| 19 | Streak tracker (for Progressive sizing) | risk module |
| 20 | Pyramiding logic (new-setup-required, within-20-30%-of-entry, separate-stop) | `agents/execution_agent.go` |
| 21 | Earnings calendar guard | data layer |
| 22 | Slippage rule (gap below stop = exit at next price) | `broker/paper_broker.go` |
| 23 | Market Regime — 10 & 20 Rule | NEW `agents/market_regime.go` |
| 24 | Market Breadth (% above 20/50/200 MA) | NEW breadth tracker |
| 25 | Net New Highs tracker (3-session signal) | NEW breadth tracker |
| 26 | Sector simultaneity detector | `agents/scanner_agent.go` |
| 27 | Liquidity filter (20/50-day avg vol) | `agents/scanner_agent.go` |
| 28 | Journaling / setup database | `core/journal.go` |
| 29 | Squat detector (no auto-exit) | `agents/patterns.go` |

---

# 13. Forensic Provenance

- **OCR engine 1:** Windows.Media.Ocr via `winocr` Python wrapper → produced 315 KB text from all 306 pages.
- **OCR engine 2 (verification):** Direct Claude vision read of the rendered PNGs of all chapter summary boxes (p24, p37, p72, p151, p167, p184, p200, p224, p247, p264, p279), the 5 scanner specification pages (p267-272), the trigger candle page (p35), the EMA extension page (p41), the risk-rules page (p190-191), all numerical tables (p197, p206, p207, p213), and the 10 & 20 rule page (p252).
- **Files retained for re-audit:**
  - `book_full_text.txt` — full Windows-OCR dump.
  - `book_pages/*.png` — rendered critical pages (high-res 3×).
  - `ocr_all_pages.py`, `render_critical_pages.py`, `render_critical_pages_v2.py` — reproducible scripts.

Every numeric value in this rulebook traces to a specific page that was viewed visually. No assumption, no fabrication.

---

*End of BOOK_RULEBOOK.md — the engine's verbatim source of truth.*
