# Earnings Cache

Drop one JSON file per symbol here, named `<SYMBOL>.json` (uppercase), e.g. `RELIANCE.json`:

```json
{"next_result_date": "2026-06-15"}
```

Source: NSE's free Corporate Filings Event Calendar
(https://www.nseindia.com/companies-listing/corporate-filings-event-calendar).

A missing or stale file is silently ignored — `EarningsCaution()` simply
returns no tag. This cache is purely advisory (book p.191 earnings-risk
caution on BUY alerts); it never affects scoring or signal decisions.
