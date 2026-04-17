package core

import (
	"database/sql"
	"log"
	"math"
	"os"
	"sync"

	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

// MLSignalFilter implements online logistic regression for signal quality prediction.
// Trains on all available journal.db files (Go project + Python sibling project).
// Features: [RSI, ADX, RVol, BB_position, VIX, AD_ratio, hour, regime]

const (
	MLFeatureCount       = 8
	MLLearningRate       = 0.01
	MLMinConfidence      = 0.30 // Lowered from 0.45 to prevent blocking every signal
	MLMinTrainingSamples = 20
)

// MLFilter holds the trained logistic regression model
type MLFilter struct {
	mu         sync.RWMutex
	Weights    [MLFeatureCount]float64
	Bias       float64
	Trained    bool
	TrainCount int
	Accuracy   float64
}

// FeatureVector represents the input features for a signal
type FeatureVector struct {
	RSI         float64
	ADX         float64
	RVol        float64
	BBPosition  float64
	VIX         float64
	ADRatio     float64
	HourOfDay   float64
	RegimeScore float64
}

func NewMLFilter() *MLFilter {
	return &MLFilter{}
}

// TrainFromJournal loads historical trades from ALL available journal.db locations
func (ml *MLFilter) TrainFromJournal() {
	// Search journal.db locations
	journalPaths := []string{
		config.JournalDB, // data/journal.db
	}

	type sample struct {
		features [MLFeatureCount]float64
		label    float64
	}
	var allSamples []sample

	for _, dbPath := range journalPaths {
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			continue
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			continue
		}

		rows, err := db.Query(`
			SELECT strategy, gross_pnl, COALESCE(rvol, 0), COALESCE(deviation_pct, 0),
			       COALESCE(regime, 'UNKNOWN'), COALESCE(entry_time, timestamp)
			FROM trades ORDER BY id ASC
		`)
		if err != nil {
			db.Close()
			continue
		}

		count := 0
		for rows.Next() {
			var strat string
			var pnl, rvol, devPct float64
			var regime, entryTime string
			rows.Scan(&strat, &pnl, &rvol, &devPct, &regime, &entryTime)

			f := [MLFeatureCount]float64{
				Clamp01(math.Abs(devPct) / 100.0),
				0.5,
				Clamp01(rvol / 5.0),
				0.5,
				0.4,
				0.5,
				encodeHour(entryTime),
				EncodeRegime(regime),
			}
			label := 0.0
			if pnl > 0 {
				label = 1.0
			}
			allSamples = append(allSamples, sample{features: f, label: label})
			count++
		}
		rows.Close()
		db.Close()

		if count > 0 {
			log.Printf("[ML] Loaded %d trades from %s", count, dbPath)
		}
	}

	if len(allSamples) < MLMinTrainingSamples {
		log.Printf("[ML] Only %d samples — need ≥%d. ML filter disabled.",
			len(allSamples), MLMinTrainingSamples)
		return
	}

	// SGD training: multiple passes
	ml.mu.Lock()
	defer ml.mu.Unlock()

	for i := range ml.Weights {
		ml.Weights[i] = 0.01
	}
	ml.Bias = 0.0

	epochs := 20
	for epoch := 0; epoch < epochs; epoch++ {
		lr := MLLearningRate / (1.0 + float64(epoch)*0.1)
		for _, s := range allSamples {
			z := ml.Bias
			for i := 0; i < MLFeatureCount; i++ {
				z += ml.Weights[i] * s.features[i]
			}
			pred := sigmoid(z)
			err := pred - s.label
			for i := 0; i < MLFeatureCount; i++ {
				ml.Weights[i] -= lr * err * s.features[i]
			}
			ml.Bias -= lr * err
		}
	}

	// Compute accuracy
	correct := 0
	for _, s := range allSamples {
		z := ml.Bias
		for i := 0; i < MLFeatureCount; i++ {
			z += ml.Weights[i] * s.features[i]
		}
		pred := sigmoid(z)
		if (pred >= 0.5 && s.label == 1.0) || (pred < 0.5 && s.label == 0.0) {
			correct++
		}
	}

	ml.Trained = true
	ml.TrainCount = len(allSamples)
	ml.Accuracy = float64(correct) / float64(len(allSamples)) * 100

	log.Printf("[ML] ═══ Trained on %d trades ═══ Accuracy=%.1f%% Bias=%.4f",
		ml.TrainCount, ml.Accuracy, ml.Bias)
}

// Predict returns probability a signal will be profitable (0.0-1.0)
func (ml *MLFilter) Predict(fv FeatureVector) float64 {
	ml.mu.RLock()
	defer ml.mu.RUnlock()
	if !ml.Trained {
		return 1.0
	}
	features := [MLFeatureCount]float64{
		fv.RSI, fv.ADX, fv.RVol, fv.BBPosition,
		fv.VIX, fv.ADRatio, fv.HourOfDay, fv.RegimeScore,
	}
	z := ml.Bias
	for i := 0; i < MLFeatureCount; i++ {
		z += ml.Weights[i] * features[i]
	}
	return sigmoid(z)
}

// ShouldTradeSignal returns (approved, probability)
func (ml *MLFilter) ShouldTradeSignal(fv FeatureVector) (bool, float64) {
	prob := ml.Predict(fv)
	return prob >= MLMinConfidence, prob
}

// ── Helper functions ────────────────────────────────────

func sigmoid(x float64) float64 {
	if x > 500 {
		return 1.0
	}
	if x < -500 {
		return 0.0
	}
	return 1.0 / (1.0 + math.Exp(-x))
}

func Clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func EncodeRegime(regime string) float64 {
	switch regime {
	case "BULL":
		return 1.0
	case "NORMAL":
		return 0.7
	case "VOLATILE":
		return 0.5
	case "CHOP":
		return 0.3
	case "BEAR_PANIC":
		return 0.1
	case "EXTREME_PANIC":
		return 0.0
	default:
		return 0.5
	}
}

func encodeHour(entryTime string) float64 {
	if len(entryTime) >= 13 {
		h := 0
		for i := 11; i < 13; i++ {
			if entryTime[i] >= '0' && entryTime[i] <= '9' {
				h = h*10 + int(entryTime[i]-'0')
			}
		}
		return Clamp01(float64(h-9) / 6.0)
	}
	return 0.5
}
