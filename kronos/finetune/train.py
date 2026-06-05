"""
NSE fine-tuning script for Kronos.

Stage 1: fine-tune tokenizer   (run first)
Stage 2: fine-tune predictor   (run after stage 1)

Usage (CPU / single GPU):
  python finetune/train.py --stage tokenizer
  python finetune/train.py --stage predictor
"""

import argparse
import json
import os
import sys
from pathlib import Path

import torch
import torch.nn.functional as F
from torch.optim import AdamW
from torch.optim.lr_scheduler import OneCycleLR
from torch.utils.data import DataLoader

# Path setup: our root first so our finetune/ package wins over Kronos repo's
OUR_ROOT    = str(Path(__file__).parent.parent)
KRONOS_REPO = os.environ.get("KRONOS_REPO", str(Path(__file__).parent.parent / "Kronos"))
sys.path.insert(0, KRONOS_REPO)
sys.path.insert(0, OUR_ROOT)

from model import Kronos, KronosTokenizer
from finetune.config import Config
from finetune.dataset import NSEDataset


def train_tokenizer(config):
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    print(f"Training tokenizer on {device}")

    tokenizer = KronosTokenizer.from_pretrained(config.pretrained_tokenizer_path).to(device)

    train_ds = NSEDataset(config, split="train")
    val_ds   = NSEDataset(config, split="val")

    train_loader = DataLoader(train_ds, batch_size=config.batch_size, shuffle=True,
                              num_workers=0, pin_memory=(device.type == "cuda"))
    val_loader   = DataLoader(val_ds,   batch_size=config.batch_size, shuffle=False,
                              num_workers=0)

    optimizer = AdamW(tokenizer.parameters(), lr=config.tokenizer_learning_rate,
                      betas=(config.adam_beta1, config.adam_beta2),
                      weight_decay=config.adam_weight_decay)
    scheduler = OneCycleLR(optimizer, max_lr=config.tokenizer_learning_rate,
                           total_steps=config.epochs * len(train_loader))

    save_path = Path(config.finetuned_tokenizer_path)
    save_path.mkdir(parents=True, exist_ok=True)
    best_val_loss = float("inf")

    for epoch in range(config.epochs):
        tokenizer.train()
        train_loss = 0.0

        for step, (x, t) in enumerate(train_loader):
            x = x.to(device)

            # Forward: returns ((z_pre, z), bsq_loss, quantized, z_indices)
            (z_pre, z), bsq_loss, _, _ = tokenizer(x)

            recon_loss = F.mse_loss(z_pre, x) + F.mse_loss(z, x)
            loss = (recon_loss + bsq_loss) / 2

            optimizer.zero_grad()
            loss.backward()
            torch.nn.utils.clip_grad_norm_(tokenizer.parameters(), config.clip)
            optimizer.step()
            scheduler.step()

            train_loss += loss.item()
            if (step + 1) % config.log_interval == 0:
                avg = train_loss / (step + 1)
                print(f"[Tokenizer] Epoch {epoch+1} Step {step+1} loss={avg:.4f}")

        # Validation
        tokenizer.eval()
        val_loss = 0.0
        with torch.no_grad():
            for x, t in val_loader:
                x = x.to(device)
                (z_pre, z), bsq_loss, _, _ = tokenizer(x)
                recon_loss = F.mse_loss(z_pre, x) + F.mse_loss(z, x)
                val_loss += ((recon_loss + bsq_loss) / 2).item()

        val_loss /= len(val_loader)
        print(f"[Tokenizer] Epoch {epoch+1} val_loss={val_loss:.4f}")

        if val_loss < best_val_loss:
            best_val_loss = val_loss
            tokenizer.save_pretrained(str(save_path))
            print(f"  Saved best tokenizer (val_loss={val_loss:.4f})")

    print(f"Tokenizer fine-tuning done. Best val_loss={best_val_loss:.4f}")


def train_predictor(config):
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    print(f"Training predictor on {device}")

    tok_path = config.finetuned_tokenizer_path if Path(config.finetuned_tokenizer_path).exists() \
               else config.pretrained_tokenizer_path
    print(f"Using tokenizer: {tok_path}")

    tokenizer = KronosTokenizer.from_pretrained(tok_path).to(device)
    tokenizer.eval()

    model = Kronos.from_pretrained(config.pretrained_predictor_path).to(device)

    train_ds = NSEDataset(config, split="train")
    val_ds   = NSEDataset(config, split="val")

    train_loader = DataLoader(train_ds, batch_size=config.batch_size, shuffle=True,
                              num_workers=0, pin_memory=(device.type == "cuda"))
    val_loader   = DataLoader(val_ds,   batch_size=config.batch_size, shuffle=False,
                              num_workers=0)

    optimizer = AdamW(model.parameters(), lr=config.predictor_learning_rate,
                      betas=(config.adam_beta1, config.adam_beta2),
                      weight_decay=config.adam_weight_decay)
    scheduler = OneCycleLR(optimizer, max_lr=config.predictor_learning_rate,
                           total_steps=config.epochs * len(train_loader))

    save_path = Path(config.finetuned_predictor_path)
    save_path.mkdir(parents=True, exist_ok=True)
    best_val_loss = float("inf")

    for epoch in range(config.epochs):
        model.train()
        train_loss = 0.0

        for step, (x, t) in enumerate(train_loader):
            x = x.to(device)
            t = t.to(device)

            with torch.no_grad():
                # encode returns (s1_indices, s2_indices) when half=True
                token_seq_0, token_seq_1 = tokenizer.encode(x, half=True)

            token_in  = [token_seq_0[:, :-1], token_seq_1[:, :-1]]
            token_out = [token_seq_0[:, 1:],  token_seq_1[:, 1:]]
            stamp_in  = t[:, :-1, :]

            logits = model(token_in[0], token_in[1], stamp_in)
            loss, _, _ = model.head.compute_loss(logits[0], logits[1],
                                                  token_out[0], token_out[1])

            optimizer.zero_grad()
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), config.clip)
            optimizer.step()
            scheduler.step()

            train_loss += loss.item()
            if (step + 1) % config.log_interval == 0:
                avg = train_loss / (step + 1)
                print(f"[Predictor] Epoch {epoch+1} Step {step+1} loss={avg:.4f}")

        # Validation
        model.eval()
        val_loss = 0.0
        with torch.no_grad():
            for x, t in val_loader:
                x = x.to(device)
                t = t.to(device)
                token_seq_0, token_seq_1 = tokenizer.encode(x, half=True)
                token_in  = [token_seq_0[:, :-1], token_seq_1[:, :-1]]
                token_out = [token_seq_0[:, 1:],  token_seq_1[:, 1:]]
                logits = model(token_in[0], token_in[1], t[:, :-1, :])
                loss, _, _ = model.head.compute_loss(logits[0], logits[1],
                                                      token_out[0], token_out[1])
                val_loss += loss.item()

        val_loss /= len(val_loader)
        print(f"[Predictor] Epoch {epoch+1} val_loss={val_loss:.4f}")

        if val_loss < best_val_loss:
            best_val_loss = val_loss
            model.save_pretrained(str(save_path))
            print(f"  Saved best predictor (val_loss={val_loss:.4f})")

    summary = {"best_val_loss": best_val_loss, "epochs": config.epochs,
               "save_path": str(save_path)}
    with open(save_path / "summary.json", "w") as f:
        json.dump(summary, f, indent=2)
    print(f"Predictor fine-tuning done. Best val_loss={best_val_loss:.4f}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", choices=["tokenizer", "predictor"], required=True)
    args = parser.parse_args()

    config = Config()

    if args.stage == "tokenizer":
        train_tokenizer(config)
    else:
        train_predictor(config)


if __name__ == "__main__":
    main()
