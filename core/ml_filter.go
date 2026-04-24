package core

import (
	"database/sql"
	"log"
	"math"
	"math/rand"
	"os"
	"sync"
	"time"

	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

// MLSignalFilter implements a State-of-the-Art Multi-Layer Perceptron (Neural Network)
// with Z-Score normalization and Adam Optimizer.
// It trains on all available journal.db trades to predict signal profitability.

const (
	MLInputSize          = 8
	MLHiddenSize         = 16
	MLLearningRate       = 0.005
	MLMinConfidence      = 0.30 // Re-enabled: 30% confidence threshold (now using real data)
	MLMinTrainingSamples = 20
	MLEpochs             = 100
)

type MLFilter struct {
	mu         sync.RWMutex
	Trained    bool
	TrainCount int
	Accuracy   float64
	Bias       float64 // Leftover from old UI struct, but we'll store loss here

	// Z-Score Normalization parameters
	featureMeans [MLInputSize]float64
	featureStds  [MLInputSize]float64

	// Neural Network Weights & Biases
	W1 [MLInputSize][MLHiddenSize]float64
	B1 [MLHiddenSize]float64
	W2 [MLHiddenSize]float64
	B2 float64
}

// FeatureVector represents the raw input features for a signal
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

// initWeights uses He initialization for ReLU layers
func (ml *MLFilter) initWeights() {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	
	// W1 (Input -> Hidden)
	std1 := math.Sqrt(2.0 / float64(MLInputSize))
	for i := 0; i < MLInputSize; i++ {
		for j := 0; j < MLHiddenSize; j++ {
			ml.W1[i][j] = rnd.NormFloat64() * std1
		}
	}
	for j := 0; j < MLHiddenSize; j++ {
		ml.B1[j] = 0.0
	}

	// W2 (Hidden -> Output)
	std2 := math.Sqrt(1.0 / float64(MLHiddenSize)) // Xavier init for Sigmoid
	for j := 0; j < MLHiddenSize; j++ {
		ml.W2[j] = rnd.NormFloat64() * std2
	}
	ml.B2 = 0.0
}

func (ml *MLFilter) TrainFromJournal() {
	journalPaths := []string{config.JournalDB}

	type sample struct {
		features [MLInputSize]float64
		label    float64
	}
	var rawSamples []sample

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
			       COALESCE(regime, 'UNKNOWN'), COALESCE(entry_time, timestamp),
			       COALESCE(rsi, 0), COALESCE(adx, 0),
			       COALESCE(vix, 0), COALESCE(ad_ratio, 0)
			FROM trades
			WHERE COALESCE(rsi, 0) > 0 OR COALESCE(adx, 0) > 0
			ORDER BY id ASC
		`)
		if err != nil {
			db.Close()
			continue
		}

		count := 0
		for rows.Next() {
			var strat string
			var pnl, rvol, devPct, rsi, adx, vix, adRatio float64
			var regime, entryTime string
			rows.Scan(&strat, &pnl, &rvol, &devPct, &regime, &entryTime, &rsi, &adx, &vix, &adRatio)

			// All 8 features now use REAL data from journal.db
			f := [MLInputSize]float64{
				Clamp01(math.Abs(devPct) / 100.0), // Deviation stretch
				Clamp01(rsi / 100.0),              // Real RSI (0-100)
				Clamp01(rvol / 5.0),               // RVol
				Clamp01(adx / 100.0),              // Real ADX (0-100)
				Clamp01(vix / 40.0),               // Real India VIX
				Clamp01(adRatio),                  // Real A/D Ratio (0-1)
				encodeHour(entryTime),
				EncodeRegime(regime),
			}
			label := 0.0
			if pnl > 0 {
				label = 1.0
			}
			rawSamples = append(rawSamples, sample{features: f, label: label})
			count++
		}
		rows.Close()
		db.Close()

		if count > 0 {
			log.Printf("[ML] Loaded %d trades for Neural Network training.", count)
		}
	}

	if len(rawSamples) < MLMinTrainingSamples {
		log.Printf("[ML] Only %d samples — need ≥%d. NN disabled.", len(rawSamples), MLMinTrainingSamples)
		return
	}

	ml.mu.Lock()
	defer ml.mu.Unlock()

	// 1. Compute Z-Score statistics (Mean and StdDev)
	for i := 0; i < MLInputSize; i++ {
		sum := 0.0
		for _, s := range rawSamples {
			sum += s.features[i]
		}
		mean := sum / float64(len(rawSamples))
		ml.featureMeans[i] = mean

		sqSum := 0.0
		for _, s := range rawSamples {
			diff := s.features[i] - mean
			sqSum += diff * diff
		}
		std := math.Sqrt(sqSum / float64(len(rawSamples)))
		if std < 1e-8 {
			std = 1.0 // prevent division by zero
		}
		ml.featureStds[i] = std
	}

	// 2. Normalize Training Data
	normSamples := make([]sample, len(rawSamples))
	for i, s := range rawSamples {
		var normF [MLInputSize]float64
		for j := 0; j < MLInputSize; j++ {
			normF[j] = (s.features[j] - ml.featureMeans[j]) / ml.featureStds[j]
		}
		normSamples[i] = sample{features: normF, label: s.label}
	}

	// 3. Initialize Neural Network
	ml.initWeights()

	// 4. Adam Optimizer State
	var mW1 [MLInputSize][MLHiddenSize]float64
	var vW1 [MLInputSize][MLHiddenSize]float64
	var mB1 [MLHiddenSize]float64
	var vB1 [MLHiddenSize]float64

	var mW2 [MLHiddenSize]float64
	var vW2 [MLHiddenSize]float64
	var mB2, vB2 float64

	beta1 := 0.9
	beta2 := 0.999
	epsilon := 1e-8

	// 5. Training Loop (Backpropagation)
	for epoch := 1; epoch <= MLEpochs; epoch++ {
		// Dynamic learning rate decay
		lr := MLLearningRate * math.Pow(0.95, float64(epoch)/10.0)
		t := float64(epoch)

		for _, s := range normSamples {
			// --- FORWARD PASS ---
			var Z1 [MLHiddenSize]float64
			var A1 [MLHiddenSize]float64
			for j := 0; j < MLHiddenSize; j++ {
				sum := ml.B1[j]
				for i := 0; i < MLInputSize; i++ {
					sum += s.features[i] * ml.W1[i][j]
				}
				Z1[j] = sum
				A1[j] = math.Max(0, sum) // ReLU
			}

			Z2 := ml.B2
			for j := 0; j < MLHiddenSize; j++ {
				Z2 += A1[j] * ml.W2[j]
			}
			A2 := sigmoid(Z2)

			// --- BACKWARD PASS ---
			// Binary Cross-Entropy Loss Derivative: dL/dZ2 = A2 - Y
			dZ2 := A2 - s.label

			// Output Layer Gradients
			dW2 := make([]float64, MLHiddenSize)
			for j := 0; j < MLHiddenSize; j++ {
				dW2[j] = dZ2 * A1[j]
			}
			dB2 := dZ2

			// Hidden Layer Gradients
			var dZ1 [MLHiddenSize]float64
			for j := 0; j < MLHiddenSize; j++ {
				dA1 := dZ2 * ml.W2[j]
				if Z1[j] > 0 { // ReLU derivative
					dZ1[j] = dA1
				} else {
					dZ1[j] = 0
				}
			}

			var dW1 [MLInputSize][MLHiddenSize]float64
			for i := 0; i < MLInputSize; i++ {
				for j := 0; j < MLHiddenSize; j++ {
					dW1[i][j] = dZ1[j] * s.features[i]
				}
			}
			dB1 := dZ1

			// --- ADAM OPTIMIZER STEP ---
			
			// Update W2, B2
			for j := 0; j < MLHiddenSize; j++ {
				mW2[j] = beta1*mW2[j] + (1-beta1)*dW2[j]
				vW2[j] = beta2*vW2[j] + (1-beta2)*(dW2[j]*dW2[j])
				m_hat := mW2[j] / (1 - math.Pow(beta1, t))
				v_hat := vW2[j] / (1 - math.Pow(beta2, t))
				ml.W2[j] -= lr * m_hat / (math.Sqrt(v_hat) + epsilon)
			}
			mB2 = beta1*mB2 + (1-beta1)*dB2
			vB2 = beta2*vB2 + (1-beta2)*(dB2*dB2)
			m_hat_B2 := mB2 / (1 - math.Pow(beta1, t))
			v_hat_B2 := vB2 / (1 - math.Pow(beta2, t))
			ml.B2 -= lr * m_hat_B2 / (math.Sqrt(v_hat_B2) + epsilon)

			// Update W1, B1
			for j := 0; j < MLHiddenSize; j++ {
				mB1[j] = beta1*mB1[j] + (1-beta1)*dB1[j]
				vB1[j] = beta2*vB1[j] + (1-beta2)*(dB1[j]*dB1[j])
				m_hat_B1 := mB1[j] / (1 - math.Pow(beta1, t))
				v_hat_B1 := vB1[j] / (1 - math.Pow(beta2, t))
				ml.B1[j] -= lr * m_hat_B1 / (math.Sqrt(v_hat_B1) + epsilon)

				for i := 0; i < MLInputSize; i++ {
					mW1[i][j] = beta1*mW1[i][j] + (1-beta1)*dW1[i][j]
					vW1[i][j] = beta2*vW1[i][j] + (1-beta2)*(dW1[i][j]*dW1[i][j])
					m_hat_W1 := mW1[i][j] / (1 - math.Pow(beta1, t))
					v_hat_W1 := vW1[i][j] / (1 - math.Pow(beta2, t))
					ml.W1[i][j] -= lr * m_hat_W1 / (math.Sqrt(v_hat_W1) + epsilon)
				}
			}
		}
	}

	// 6. Compute Final Accuracy
	correct := 0
	totalLoss := 0.0
	for _, s := range normSamples {
		pred := ml.predictNormalized(s.features)
		if (pred >= 0.5 && s.label == 1.0) || (pred < 0.5 && s.label == 0.0) {
			correct++
		}
		// Binary cross entropy
		p := math.Max(1e-15, math.Min(1-1e-15, pred))
		if s.label == 1.0 {
			totalLoss -= math.Log(p)
		} else {
			totalLoss -= math.Log(1 - p)
		}
	}

	ml.Trained = true
	ml.TrainCount = len(rawSamples)
	ml.Accuracy = float64(correct) / float64(len(rawSamples)) * 100
	ml.Bias = totalLoss / float64(len(rawSamples)) // Storing avg loss here for display

	log.Printf("[ML] ═══ SOTA Neural Network Trained ═══ Trades: %d | Acc: %.1f%% | Loss: %.4f",
		ml.TrainCount, ml.Accuracy, ml.Bias)
}

// predictNormalized performs a forward pass with normalized features
func (ml *MLFilter) predictNormalized(features [MLInputSize]float64) float64 {
	var A1 [MLHiddenSize]float64
	for j := 0; j < MLHiddenSize; j++ {
		sum := ml.B1[j]
		for i := 0; i < MLInputSize; i++ {
			sum += features[i] * ml.W1[i][j]
		}
		A1[j] = math.Max(0, sum) // ReLU
	}

	Z2 := ml.B2
	for j := 0; j < MLHiddenSize; j++ {
		Z2 += A1[j] * ml.W2[j]
	}
	return sigmoid(Z2)
}

// Predict returns probability a signal will be profitable (0.0-1.0)
func (ml *MLFilter) Predict(fv FeatureVector) float64 {
	ml.mu.RLock()
	defer ml.mu.RUnlock()
	if !ml.Trained {
		return 1.0
	}
	
	rawFeatures := [MLInputSize]float64{
		fv.RSI, fv.ADX, fv.RVol, fv.BBPosition,
		fv.VIX, fv.ADRatio, fv.HourOfDay, fv.RegimeScore,
	}

	// Apply Z-Score normalization based on training stats
	var normF [MLInputSize]float64
	for i := 0; i < MLInputSize; i++ {
		normF[i] = (rawFeatures[i] - ml.featureMeans[i]) / ml.featureStds[i]
	}

	return ml.predictNormalized(normF)
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
	if x < 0 { return 0 }
	if x > 1 { return 1 }
	return x
}

func EncodeRegime(regime string) float64 {
	switch regime {
	case "BULL": return 1.0
	case "NORMAL": return 0.7
	case "VOLATILE": return 0.5
	case "CHOP": return 0.3
	case "BEAR_PANIC": return 0.1
	case "EXTREME_PANIC": return 0.0
	default: return 0.5
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
