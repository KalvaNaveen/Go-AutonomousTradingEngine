"""
Kronos FastAPI microservice — called by the Zenith Go engine after EOD scan.

Endpoints:
  POST /predict_batch  — rank a list of BUY signals by predicted 5-day upside
  GET  /health         — liveness check

The Go engine sends:
  { "signals": [ { "symbol": "DIXON", "ohlcv": [{date,open,high,low,close,volume},...] }, ... ] }

Returns:
  { "ranked": [ { "symbol": "DIXON", "predicted_close_5d": 2450.5, "upside_pct": 4.2 }, ... ] }
  sorted by upside_pct descending.
"""

import os, sys, logging
from pathlib import Path
from typing import List, Optional

import numpy as np
import pandas as pd
import torch
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
import uvicorn

# ── Kronos imports (cloned repo must be on PYTHONPATH) ─────────────────────
KRONOS_REPO = os.environ.get("KRONOS_REPO", str(Path(__file__).parent / "Kronos"))
sys.path.insert(0, KRONOS_REPO)

try:
    from model import Kronos, KronosTokenizer, KronosPredictor
    KRONOS_AVAILABLE = True
except ImportError as e:
    logging.warning(f"Kronos not available: {e}. Service will return fallback scores.")
    KRONOS_AVAILABLE = False

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("kronos-service")

app = FastAPI(title="Kronos Signal Ranker", version="1.0")

# ── Model config ───────────────────────────────────────────────────────────
MODELS_DIR       = Path(__file__).parent / "models"
TOKENIZER_PATH   = os.environ.get("KRONOS_TOKENIZER", str(MODELS_DIR / "nse_tokenizer"))
PREDICTOR_PATH   = os.environ.get("KRONOS_PREDICTOR", str(MODELS_DIR / "nse_predictor_base"))
PRED_LEN         = 5      # forecast 5 trading days forward
LOOKBACK         = 90     # use last 90 days as context
SAMPLE_COUNT     = 5      # average 5 sample paths for stability
TEMPERATURE      = 0.6
TOP_P            = 0.9

predictor: Optional[KronosPredictor] = None

def load_model():
    global predictor
    if not KRONOS_AVAILABLE:
        return

    tok_path  = TOKENIZER_PATH if Path(TOKENIZER_PATH).exists() else "NeoQuasar/Kronos-Tokenizer-base"
    pred_path = PREDICTOR_PATH if Path(PREDICTOR_PATH).exists() else "NeoQuasar/Kronos-base"

    log.info(f"Loading tokenizer from {tok_path}")
    log.info(f"Loading predictor from {pred_path}")

    tokenizer = KronosTokenizer.from_pretrained(tok_path)
    model     = Kronos.from_pretrained(pred_path)
    predictor = KronosPredictor(model, tokenizer, max_context=512)
    log.info("Kronos model loaded ✅")

@app.on_event("startup")
def startup():
    load_model()

# ── Request / Response schemas ─────────────────────────────────────────────

class OHLCVBar(BaseModel):
    date:   str
    open:   float
    high:   float
    low:    float
    close:  float
    volume: float = 0.0

class SignalInput(BaseModel):
    symbol: str
    ohlcv:  List[OHLCVBar]

class PredictBatchRequest(BaseModel):
    signals: List[SignalInput]

class RankedSignal(BaseModel):
    symbol:             str
    predicted_close_5d: float
    current_close:      float
    upside_pct:         float

class PredictBatchResponse(BaseModel):
    ranked: List[RankedSignal]
    model:  str

# ── Prediction logic ───────────────────────────────────────────────────────

def _bars_to_df(bars: List[OHLCVBar]) -> pd.DataFrame:
    df = pd.DataFrame([b.model_dump() for b in bars])
    df["date"] = pd.to_datetime(df["date"])
    df = df.sort_values("date").reset_index(drop=True)
    df = df.rename(columns={"volume": "vol"})
    # amt = avg price × volume (Kronos feature)
    df["amt"] = (df["open"] + df["high"] + df["low"] + df["close"]) / 4 * df["vol"]
    return df

def _predict_one(df: pd.DataFrame) -> float:
    """Return predicted close price PRED_LEN bars ahead."""
    if len(df) < LOOKBACK:
        return float(df["close"].iloc[-1])  # not enough history — return current

    x_df = df[["open", "high", "low", "close", "vol", "amt"]].iloc[-LOOKBACK:].reset_index(drop=True)

    # KronosPredictor.predict() requires pd.Series for timestamps (not DatetimeIndex).
    last_date = df["date"].iloc[-1]
    x_ts = pd.Series(pd.date_range(end=last_date, periods=LOOKBACK, freq="B"))
    y_ts = pd.Series(pd.date_range(start=last_date, periods=PRED_LEN + 1, freq="B")[1:])

    pred_df = predictor.predict(
        df=x_df,
        x_timestamp=x_ts,
        y_timestamp=y_ts,
        pred_len=PRED_LEN,
        T=TEMPERATURE,
        top_p=TOP_P,
        sample_count=SAMPLE_COUNT,
    )
    return float(pred_df["close"].iloc[-1])

# ── Endpoints ──────────────────────────────────────────────────────────────

@app.get("/health")
def health():
    return {
        "status": "ok",
        "model_loaded": predictor is not None,
        "model_path": PREDICTOR_PATH,
    }

@app.post("/predict_batch", response_model=PredictBatchResponse)
def predict_batch(req: PredictBatchRequest):
    if not req.signals:
        raise HTTPException(400, "No signals provided")

    results = []
    model_label = "Kronos-NSE-finetuned" if Path(PREDICTOR_PATH).exists() else "Kronos-small-pretrained"

    for sig in req.signals:
        df = _bars_to_df(sig.ohlcv)
        current_close = float(df["close"].iloc[-1])

        if predictor is not None:
            try:
                pred_close = _predict_one(df)
            except Exception as e:
                log.warning(f"{sig.symbol} prediction failed: {e} — using current close")
                pred_close = current_close
        else:
            # Kronos not available — return neutral (upside=0) so Go still gets a response
            pred_close = current_close

        upside = (pred_close - current_close) / current_close * 100 if current_close > 0 else 0.0

        results.append(RankedSignal(
            symbol=sig.symbol,
            predicted_close_5d=round(pred_close, 4),
            current_close=round(current_close, 4),
            upside_pct=round(upside, 2),
        ))

    # Sort by predicted upside descending
    results.sort(key=lambda r: r.upside_pct, reverse=True)
    log.info(f"Ranked {len(results)} signals. Top: {results[0].symbol} ({results[0].upside_pct:+.1f}%)")

    return PredictBatchResponse(ranked=results, model=model_label)

if __name__ == "__main__":
    port = int(os.environ.get("KRONOS_PORT", 8765))
    uvicorn.run("service:app", host="0.0.0.0", port=port, reload=False)
