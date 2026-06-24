// Package gate implements the two-tier BTST sentiment guard:
//
//	Tier 1 (macro) — skip the WHOLE day if the market regime is risk-off.
//	Tier 2 (news)  — drop INDIVIDUAL stocks with bad company-specific headlines.
//
// All data sources are free and require no auth (Yahoo index quotes, Google
// News RSS), chosen for reliability over a paid feed.
package gate

import (
	"context"
	"fmt"
	"strings"

	"bnf_go_engine/config"
	"bnf_go_engine/quotes"
)

// indexSource fetches index OHLC (satisfied by *quotes.Client).
type indexSource interface {
	Index(ctx context.Context, symbol string) (quotes.OHLC, error)
}

// Macro is the Tier-1 day-level gate.
type Macro struct {
	q indexSource
}

// NewMacro builds the macro gate over a Yahoo index source.
func NewMacro(q indexSource) *Macro { return &Macro{q: q} }

// Yahoo index symbols.
const (
	symVIX   = "^INDIAVIX"
	symNifty = "^NSEI"
)

// Check returns ok=false (with a human reason) when the day should be skipped.
//
// Two reliable, free signals are evaluated:
//   - India VIX above BTSTVIXMax  → elevated fear, skip.
//   - Nifty 50 down > BTSTNiftyDropPct intraday → weak tape, skip.
//
// NOTE on GIFT Nifty: it is the ideal pre-open predictor of *tomorrow's* gap,
// but there is no reliable free/official API for it (NSE-IX requires a paid
// feed; third-party scrapes break often). Rather than ship a fragile or
// fabricated source, it is intentionally omitted. VIX + Nifty trend are the
// dependable free proxies. If a paid GIFT feed is added later, plug it in here.
func (m *Macro) Check(ctx context.Context) (ok bool, reason string) {
	if !config.BTSTGateEnabled {
		return true, "gate disabled"
	}

	// India VIX
	if vix, err := m.q.Index(ctx, symVIX); err != nil {
		// Fail-open on data error but say so — we don't want a Yahoo hiccup to
		// silently block every trade. The user confirmed neutral/unknown = TRADE.
		reason = fmt.Sprintf("VIX unavailable (%v) — proceeding", err)
	} else if vix.Last > config.BTSTVIXMax {
		return false, fmt.Sprintf("India VIX %.1f > %.1f (elevated fear)", vix.Last, config.BTSTVIXMax)
	}

	// Nifty 50 intraday change
	if nifty, err := m.q.Index(ctx, symNifty); err != nil {
		if reason != "" {
			reason += "; "
		}
		reason += fmt.Sprintf("Nifty unavailable (%v) — proceeding", err)
	} else if nifty.PrevClose > 0 {
		chg := (nifty.Last - nifty.PrevClose) / nifty.PrevClose * 100
		if chg < -config.BTSTNiftyDropPct {
			return false, fmt.Sprintf("Nifty %.2f%% (down > %.1f%%)", chg, config.BTSTNiftyDropPct)
		}
	}

	if reason == "" {
		reason = "ok"
	}
	return true, strings.TrimSpace(reason)
}
