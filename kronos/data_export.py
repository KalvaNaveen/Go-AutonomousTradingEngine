"""
NSE Data Exporter — fetches 5yr daily OHLCV from Kite historical API
for all Nifty 750 stocks and saves as pickle files for Kronos fine-tuning.

Usage:
  python data_export.py --api-key XXXX --access-token YYYY

Output:
  kronos/data/train_data.pkl   (2011-01-01 to 2024-12-31)
  kronos/data/val_data.pkl     (2023-01-01 to 2024-12-31)
  kronos/data/test_data.pkl    (2025-01-01 to today)
  kronos/data/instruments.csv  (token → symbol mapping)
"""

import argparse
import csv
import json
import pickle
import time
from datetime import datetime, date, timedelta
from pathlib import Path

import pandas as pd
import requests
from tqdm import tqdm

BASE_URL    = "https://api.kite.trade"
NSE_CSV_URL = "https://archives.nseindia.com/content/indices/ind_nifty500list.csv"
DATA_DIR    = Path(__file__).parent / "data"
DATA_DIR.mkdir(exist_ok=True)

TRAIN_END = "2023-12-31"
VAL_START = "2023-01-01"
VAL_END   = "2024-12-31"
TEST_START = "2025-01-01"
FETCH_FROM = "2019-01-01"   # 6 years of history

# ── Kite helpers ───────────────────────────────────────────────────────────

def kite_headers(api_key, access_token):
    return {
        "X-Kite-Version": "3",
        "Authorization": f"token {api_key}:{access_token}",
    }

def fetch_instruments(api_key, access_token):
    resp = requests.get(f"{BASE_URL}/instruments/NSE", headers=kite_headers(api_key, access_token), timeout=30)
    lines = resp.text.strip().splitlines()
    reader = csv.DictReader(lines)
    return list(reader)

def fetch_ohlcv(token, from_date, to_date, api_key, access_token, retries=3):
    url = f"{BASE_URL}/instruments/historical/{token}/day?from={from_date}&to={to_date}"
    for attempt in range(retries):
        try:
            resp = requests.get(url, headers=kite_headers(api_key, access_token), timeout=15)
            data = resp.json()
            if data.get("status") != "success":
                return None
            candles = data["data"]["candles"]
            if not candles:
                return None
            rows = []
            for c in candles:
                if len(c) < 6:
                    continue
                rows.append({
                    "date":   pd.Timestamp(c[0][:10]),
                    "open":   float(c[1]),
                    "high":   float(c[2]),
                    "low":    float(c[3]),
                    "close":  float(c[4]),
                    "vol":    float(c[5]),
                })
            df = pd.DataFrame(rows).set_index("date")
            df["amt"] = (df["open"] + df["high"] + df["low"] + df["close"]) / 4 * df["vol"]
            return df
        except Exception as e:
            if attempt < retries - 1:
                time.sleep(1)
    return None

# ── NSE universe loader ────────────────────────────────────────────────────

def load_nse_universe():
    try:
        resp = requests.get(NSE_CSV_URL, headers={"User-Agent": "Mozilla/5.0"}, timeout=15)
        lines = resp.text.strip().splitlines()
        reader = csv.DictReader(lines)
        return {row["Symbol"].strip() for row in reader if row.get("Symbol")}
    except Exception as e:
        print(f"Warning: NSE CSV fetch failed ({e}), falling back to instruments.csv")
        csv_path = DATA_DIR / "instruments.csv"
        if csv_path.exists():
            df = pd.read_csv(csv_path)
            return set(df["symbol"].tolist())
        return set()

# ── Main ───────────────────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--api-key",      required=True)
    parser.add_argument("--access-token", required=True)
    parser.add_argument("--from-date",    default=FETCH_FROM)
    parser.add_argument("--max-stocks",   type=int, default=0, help="0 = all")
    args = parser.parse_args()

    print("Loading NSE universe...")
    nse_symbols = load_nse_universe()
    print(f"  {len(nse_symbols)} symbols from NSE")

    print("Fetching Kite instruments...")
    instruments = fetch_instruments(args.api_key, args.access_token)
    universe = {}
    for inst in instruments:
        sym = inst.get("tradingsymbol", "")
        if inst.get("instrument_type") == "EQ" and inst.get("segment") == "NSE" and sym in nse_symbols:
            universe[int(inst["instrument_token"])] = sym

    print(f"  {len(universe)} EQ tokens matched")

    if args.max_stocks > 0:
        universe = dict(list(universe.items())[:args.max_stocks])
        print(f"  Limited to {len(universe)} for testing")

    # Save token→symbol map
    inst_df = pd.DataFrame({"token": list(universe.keys()), "symbol": list(universe.values())})
    inst_df.to_csv(DATA_DIR / "instruments.csv", index=False)

    today = date.today().isoformat()
    all_data = {}
    failed = []

    for token, symbol in tqdm(universe.items(), desc="Fetching OHLCV"):
        df = fetch_ohlcv(token, args.from_date, today, args.api_key, args.access_token)
        if df is None or len(df) < 100:
            failed.append(symbol)
        else:
            df = df.dropna()
            all_data[symbol] = df
        time.sleep(0.35)  # Kite rate limit

    print(f"\nFetched {len(all_data)} stocks. Failed: {len(failed)}")
    if failed:
        print(f"  Failed: {failed[:10]}{'...' if len(failed)>10 else ''}")

    # Split into train / val / test
    def split(data, start, end):
        result = {}
        for sym, df in data.items():
            subset = df[(df.index >= start) & (df.index <= end)]
            if len(subset) >= 50:
                result[sym] = subset
        return result

    train = split(all_data, FETCH_FROM, TRAIN_END)
    val   = split(all_data, VAL_START,  VAL_END)
    test  = split(all_data, TEST_START,  today)

    print(f"Train: {len(train)} stocks | Val: {len(val)} | Test: {len(test)}")

    with open(DATA_DIR / "train_data.pkl", "wb") as f: pickle.dump(train, f)
    with open(DATA_DIR / "val_data.pkl",   "wb") as f: pickle.dump(val,   f)
    with open(DATA_DIR / "test_data.pkl",  "wb") as f: pickle.dump(test,  f)

    print(f"Saved to {DATA_DIR}/")
    print("  train_data.pkl  val_data.pkl  test_data.pkl")

if __name__ == "__main__":
    main()
