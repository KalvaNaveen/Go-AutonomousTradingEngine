package storage

import (
	"sync"
	"sync/atomic"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/config"
)

// TickStore — central in-memory store for live market data.
// Port of Python storage/tick_store.py with lock-free atomic reads.
type TickStore struct {
	// Per-token data (lock-free reads via atomic)
	store sync.Map // token(uint32) -> *TickData

	// VWAP data
	vwap sync.Map // token(uint32) -> *VWAPData

	// ORB data
	orb sync.Map // token(uint32) -> *ORBData

	ready        int32     // atomic flag
	lastTickAt   time.Time
	lastTickAtMu sync.Mutex
}

type TickData struct {
	mu          sync.Mutex
	LastPrice   float64
	Volume      int64
	DayOpen     float64
	DayHigh     float64
	DayLow      float64
	ChangePct   float64
	BidAskRatio float64
	BidQty      int64
	AskQty      int64
	LastTickAt  time.Time
	prevVolume  int64 // track previous total volume to compute incremental for candles

	// 5-min candles
	Candles5Min    []agents.Candle
	currentCandle  *candleBuilder
}

type candleBuilder struct {
	bucket time.Time
	open   float64
	high   float64
	low    float64
	close  float64
	volume int64
}

type VWAPData struct {
	mu      sync.Mutex
	CumPV   float64
	CumVol  int64
	VWAP    float64
	prevVol int64 // track previous total volume to compute incremental
}

type ORBData struct {
	mu       sync.Mutex
	ORBHigh  float64
	ORBLow   float64
	ORBLocked bool
}

func NewTickStore() *TickStore {
	return &TickStore{}
}

// getOrCreate gets existing TickData or creates new
func (ts *TickStore) getOrCreate(token uint32) *TickData {
	val, ok := ts.store.Load(token)
	if ok {
		return val.(*TickData)
	}
	td := &TickData{}
	actual, _ := ts.store.LoadOrStore(token, td)
	return actual.(*TickData)
}

func (ts *TickStore) getVWAP(token uint32) *VWAPData {
	val, ok := ts.vwap.Load(token)
	if ok {
		return val.(*VWAPData)
	}
	vd := &VWAPData{}
	actual, _ := ts.vwap.LoadOrStore(token, vd)
	return actual.(*VWAPData)
}

func (ts *TickStore) getORB(token uint32) *ORBData {
	val, ok := ts.orb.Load(token)
	if ok {
		return val.(*ORBData)
	}
	od := &ORBData{ORBLow: 999999.0}
	actual, _ := ts.orb.LoadOrStore(token, od)
	return actual.(*ORBData)
}

// bucket5Min rounds timestamp down to nearest 5-min boundary
func bucket5Min(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), (t.Minute()/5)*5, 0, 0, t.Location())
}

// OnTick processes a single tick — called from WebSocket goroutine
func (ts *TickStore) OnTick(token uint32, ltp float64, vol int64, dayOpen, dayHigh, dayLow, changePct float64, bidQty, askQty int64, exchangeTS time.Time) {
	td := ts.getOrCreate(token)

	td.mu.Lock()
	if ltp > 0 {
		td.LastPrice = ltp
		td.LastTickAt = config.NowIST()
	}
	if vol > 0 {
		td.Volume = vol
	}
	td.ChangePct = changePct
	if dayOpen > 0 {
		td.DayOpen = dayOpen
	}
	if dayHigh > 0 {
		td.DayHigh = dayHigh
	}
	if dayLow > 0 {
		td.DayLow = dayLow
	}
	if askQty > 0 || bidQty > 0 {
		td.BidQty = bidQty
		td.AskQty = askQty
		if askQty > 0 {
			td.BidAskRatio = float64(bidQty) / float64(askQty)
		} else {
			td.BidAskRatio = 1.0
		}
	}

	// 5-min candle building
	if ltp > 0 && !exchangeTS.IsZero() {
		// Compute incremental volume (Kite sends total day volume, not per-tick)
		deltaVol := vol - td.prevVolume
		if deltaVol < 0 {
			deltaVol = vol // Day reset or first tick
		}
		td.prevVolume = vol

		bucket := bucket5Min(exchangeTS)
		if td.currentCandle == nil || td.currentCandle.bucket != bucket {
			// Close existing candle
			if td.currentCandle != nil {
				td.Candles5Min = append(td.Candles5Min, agents.Candle{
					Open:   td.currentCandle.open,
					High:   td.currentCandle.high,
					Low:    td.currentCandle.low,
					Close:  td.currentCandle.close,
					Volume: td.currentCandle.volume,
				})
				if len(td.Candles5Min) > 250 {
					td.Candles5Min = td.Candles5Min[len(td.Candles5Min)-250:]
				}
			}
			td.currentCandle = &candleBuilder{
				bucket: bucket, open: ltp, high: ltp, low: ltp, close: ltp, volume: deltaVol,
			}
		} else {
			if ltp > td.currentCandle.high {
				td.currentCandle.high = ltp
			}
			if ltp < td.currentCandle.low {
				td.currentCandle.low = ltp
			}
			td.currentCandle.close = ltp
			td.currentCandle.volume += deltaVol
		}
	}
	td.mu.Unlock()

	// VWAP update — Kite FULL mode sends TOTAL day volume, not incremental.
	// We must compute the delta ourselves to get a correct VWAP.
	if ltp > 0 && vol > 0 {
		vd := ts.getVWAP(token)
		vd.mu.Lock()
		incrementalVol := vol - vd.prevVol
		if incrementalVol > 0 {
			vd.CumPV += ltp * float64(incrementalVol)
			vd.CumVol += incrementalVol
			if vd.CumVol > 0 {
				vd.VWAP = vd.CumPV / float64(vd.CumVol)
			}
		}
		vd.prevVol = vol
		vd.mu.Unlock()
	}

	// ORB tracking (first 15 minutes: 9:15-9:30)
	if ltp > 0 && !exchangeTS.IsZero() {
		od := ts.getORB(token)
		od.mu.Lock()
		if !od.ORBLocked {
			h, m := exchangeTS.Hour(), exchangeTS.Minute()
			if h == 9 && m < 30 {
				if ltp > od.ORBHigh {
					od.ORBHigh = ltp
				}
				if ltp < od.ORBLow {
					od.ORBLow = ltp
				}
			} else {
				od.ORBLocked = true
			}
		}
		od.mu.Unlock()
	}

	atomic.StoreInt32(&ts.ready, 1)
	ts.lastTickAtMu.Lock()
	ts.lastTickAt = config.NowIST()
	ts.lastTickAtMu.Unlock()
}

// ── Read Interface (lock-free for hot path) ──────────────────

const StaleThresholdSecs = 60 // 60s: less liquid Nifty 250 stocks may not tick for 30s+

func (ts *TickStore) GetLTP(token uint32) float64 {
	td := ts.getOrCreate(token)
	td.mu.Lock()
	defer td.mu.Unlock()
	return td.LastPrice
}

func (ts *TickStore) GetLTPIfFresh(token uint32) float64 {
	td := ts.getOrCreate(token)
	td.mu.Lock()
	ltp := td.LastPrice
	tickAt := td.LastTickAt
	td.mu.Unlock()

	if tickAt.IsZero() {
		return 0.0
	}
	if config.NowIST().Sub(tickAt).Seconds() > StaleThresholdSecs {
		return 0.0
	}
	return ltp
}

func (ts *TickStore) GetVolume(token uint32) int64 {
	td := ts.getOrCreate(token)
	td.mu.Lock()
	defer td.mu.Unlock()
	return td.Volume
}

func (ts *TickStore) GetDayOpen(token uint32) float64 {
	td := ts.getOrCreate(token)
	td.mu.Lock()
	defer td.mu.Unlock()
	return td.DayOpen
}

func (ts *TickStore) GetDepth(token uint32) map[string]float64 {
	td := ts.getOrCreate(token)
	td.mu.Lock()
	defer td.mu.Unlock()
	return map[string]float64{
		"bid_qty":       float64(td.BidQty),
		"ask_qty":       float64(td.AskQty),
		"bid_ask_ratio": td.BidAskRatio,
	}
}

func (ts *TickStore) GetCandles5Min(token uint32) []agents.Candle {
	td := ts.getOrCreate(token)
	td.mu.Lock()
	defer td.mu.Unlock()

	result := make([]agents.Candle, len(td.Candles5Min))
	copy(result, td.Candles5Min)

	// Append current in-progress candle
	if td.currentCandle != nil {
		result = append(result, agents.Candle{
			Open:   td.currentCandle.open,
			High:   td.currentCandle.high,
			Low:    td.currentCandle.low,
			Close:  td.currentCandle.close,
			Volume: td.currentCandle.volume,
		})
	}
	return result
}

func (ts *TickStore) GetVWAP(token uint32) float64 {
	vd := ts.getVWAP(token)
	vd.mu.Lock()
	defer vd.mu.Unlock()
	return vd.VWAP
}

func (ts *TickStore) GetORB(token uint32) (float64, float64) {
	od := ts.getORB(token)
	od.mu.Lock()
	defer od.mu.Unlock()
	if od.ORBHigh > 0 && od.ORBLow < 999999.0 {
		return od.ORBHigh, od.ORBLow
	}
	return 0, 0
}

func (ts *TickStore) IsReady() bool {
	return atomic.LoadInt32(&ts.ready) == 1
}

func (ts *TickStore) IsFresh() bool {
	ts.lastTickAtMu.Lock()
	t := ts.lastTickAt
	ts.lastTickAtMu.Unlock()
	if t.IsZero() {
		return false
	}
	return config.NowIST().Sub(t).Seconds() <= StaleThresholdSecs
}

func (ts *TickStore) ResetDaily() {
	ts.vwap = sync.Map{}
	ts.orb = sync.Map{}
	ts.store.Range(func(key, value interface{}) bool {
		td := value.(*TickData)
		td.mu.Lock()
		td.DayOpen = 0
		td.DayHigh = 0
		td.DayLow = 0
		td.Volume = 0
		td.prevVolume = 0    // Reset incremental volume tracking
		td.Candles5Min = nil    // Clear stale candles from previous day
		td.currentCandle = nil // Reset in-progress candle
		td.mu.Unlock()
		return true
	})
}

// GetAdvanceCount returns (advancing, declining) for given tokens
func (ts *TickStore) GetAdvanceCount(tokens []uint32) (int, int) {
	adv, dec := 0, 0
	for _, token := range tokens {
		td := ts.getOrCreate(token)
		td.mu.Lock()
		chg := td.ChangePct
		td.mu.Unlock()
		if chg > 0 {
			adv++
		} else if chg < 0 {
			dec++
		}
	}
	return adv, dec
}
