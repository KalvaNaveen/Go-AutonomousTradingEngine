"""
NSE Dataset — loads Kite-exported pickle files (no Qlib dependency).
Mirrors the interface of Kronos finetune/dataset.py so the same
train_predictor.py training loop works unchanged.
"""

import pickle
import random
from pathlib import Path

import numpy as np
import pandas as pd
import torch
from torch.utils.data import Dataset


class NSEDataset(Dataset):
    def __init__(self, config, split="train"):
        self.config = config
        self.lw = config.lookback_window
        self.pw = config.predict_window
        window = self.lw + self.pw + 1

        pkl_file = {"train": "train_data.pkl", "val": "val_data.pkl", "test": "test_data.pkl"}[split]
        with open(Path(config.dataset_path) / pkl_file, "rb") as f:
            raw: dict = pickle.load(f)

        self.samples = []  # list of (feature_array, time_feature_array)

        for symbol, df in raw.items():
            df = df[config.feature_list].dropna()
            if len(df) < window:
                continue

            # Time features from index
            idx = df.index
            tfeats = pd.DataFrame({
                "weekday": idx.weekday,
                "day":     idx.day,
                "month":   idx.month,
            }, index=idx).values.astype(np.float32)

            vals = df.values.astype(np.float32)

            for i in range(len(df) - window + 1):
                x = vals[i: i + window]
                t = tfeats[i: i + window]
                self.samples.append((x, t))

        random.shuffle(self.samples)
        print(f"[NSEDataset] {split}: {len(self.samples)} windows from {len(raw)} stocks")

    def __len__(self):
        return len(self.samples)

    def __getitem__(self, idx):
        x, t = self.samples[idx]
        lw, pw = self.lw, self.pw

        # Normalize on lookback window only (no future leakage)
        hist = x[:lw]
        mean = hist.mean(axis=0)
        std  = hist.std(axis=0) + 1e-5

        x_norm = (x - mean) / std
        x_norm = np.clip(x_norm, -10, 10)

        return (
            torch.tensor(x_norm, dtype=torch.float32),
            torch.tensor(t,      dtype=torch.float32),
        )

    def set_epoch_seed(self, epoch):
        random.seed(epoch)
