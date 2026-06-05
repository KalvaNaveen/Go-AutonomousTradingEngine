"""
NSE fine-tuning script for Kronos.

Stage 1: fine-tune tokenizer   (run first)
Stage 2: fine-tune predictor   (run after stage 1)

Usage (single GPU):
  python train.py --stage tokenizer
  python train.py --stage predictor

Usage (multi-GPU):
  torchrun --standalone --nproc_per_node=2 train.py --stage tokenizer
  torchrun --standalone --nproc_per_node=2 train.py --stage predictor
"""

import argparse
import json
import os
import sys
import time
from pathlib import Path

import torch
import torch.distributed as dist
from torch.nn.parallel import DistributedDataParallel as DDP
from torch.optim import AdamW
from torch.optim.lr_scheduler import OneCycleLR
from torch.utils.data import DataLoader
from torch.utils.data.distributed import DistributedSampler

# Add Kronos repo to path
KRONOS_REPO = os.environ.get("KRONOS_REPO", str(Path(__file__).parent.parent / "Kronos"))
sys.path.insert(0, KRONOS_REPO)

from model import Kronos, KronosTokenizer
from finetune.config import Config
from finetune.dataset import NSEDataset


def setup_distributed():
    if "LOCAL_RANK" in os.environ:
        local_rank = int(os.environ["LOCAL_RANK"])
        dist.init_process_group("nccl")
        torch.cuda.set_device(local_rank)
        return local_rank, dist.get_world_size(), True
    return 0, 1, False


def is_main(rank):
    return rank == 0


def train_tokenizer(config, rank, world_size, distributed):
    device = torch.device(f"cuda:{rank}" if torch.cuda.is_available() else "cpu")

    tokenizer = KronosTokenizer.from_pretrained(config.pretrained_tokenizer_path)
    tokenizer = tokenizer.to(device)

    if distributed:
        tokenizer = DDP(tokenizer, device_ids=[rank])

    train_ds = NSEDataset(config, split="train")
    val_ds   = NSEDataset(config, split="val")

    train_sampler = DistributedSampler(train_ds) if distributed else None
    train_loader  = DataLoader(train_ds, batch_size=config.batch_size,
                               sampler=train_sampler, shuffle=(train_sampler is None),
                               num_workers=2, pin_memory=True)
    val_loader    = DataLoader(val_ds, batch_size=config.batch_size,
                               shuffle=False, num_workers=2)

    optimizer = AdamW(tokenizer.parameters(), lr=config.tokenizer_learning_rate,
                      betas=(config.adam_beta1, config.adam_beta2),
                      weight_decay=config.adam_weight_decay)
    scheduler = OneCycleLR(optimizer, max_lr=config.tokenizer_learning_rate,
                           total_steps=config.epochs * len(train_loader))

    save_path = Path(config.finetuned_tokenizer_path)
    save_path.mkdir(parents=True, exist_ok=True)

    best_val_loss = float("inf")

    for epoch in range(config.epochs):
        if train_sampler:
            train_sampler.set_epoch(epoch)

        tokenizer.train()
        train_loss = 0.0
        for step, (x, t) in enumerate(train_loader):
            x, t = x.to(device), t.to(device)

            out   = tokenizer(x) if not distributed else tokenizer.module(x)
            loss  = out.loss if hasattr(out, "loss") else out[0]

            loss.backward()
            torch.nn.utils.clip_grad_norm_(tokenizer.parameters(), config.clip)
            optimizer.step()
            scheduler.step()
            optimizer.zero_grad()

            train_loss += loss.item()

            if is_main(rank) and (step + 1) % config.log_interval == 0:
                print(f"[Tokenizer] Epoch {epoch+1} Step {step+1} "
                      f"loss={train_loss/(step+1):.4f}")

        # Validation
        tokenizer.eval()
        val_loss = 0.0
        with torch.no_grad():
            for x, t in val_loader:
                x, t = x.to(device), t.to(device)
                out  = tokenizer(x) if not distributed else tokenizer.module(x)
                loss = out.loss if hasattr(out, "loss") else out[0]
                val_loss += loss.item()

        val_loss /= len(val_loader)
        if is_main(rank):
            print(f"[Tokenizer] Epoch {epoch+1} val_loss={val_loss:.4f}")
            if val_loss < best_val_loss:
                best_val_loss = val_loss
                m = tokenizer.module if distributed else tokenizer
                m.save_pretrained(str(save_path))
                print(f"  ✅ Saved best tokenizer (val_loss={val_loss:.4f})")

    if is_main(rank):
        print(f"Tokenizer fine-tuning done. Best val_loss={best_val_loss:.4f}")


def train_predictor(config, rank, world_size, distributed):
    device = torch.device(f"cuda:{rank}" if torch.cuda.is_available() else "cpu")

    tok_path  = config.finetuned_tokenizer_path if Path(config.finetuned_tokenizer_path).exists() \
                else config.pretrained_tokenizer_path
    pred_path = config.pretrained_predictor_path

    tokenizer = KronosTokenizer.from_pretrained(tok_path).to(device)
    tokenizer.eval()

    model = Kronos.from_pretrained(pred_path).to(device)
    if distributed:
        model = DDP(model, device_ids=[rank])

    train_ds = NSEDataset(config, split="train")
    val_ds   = NSEDataset(config, split="val")

    train_sampler = DistributedSampler(train_ds) if distributed else None
    train_loader  = DataLoader(train_ds, batch_size=config.batch_size,
                               sampler=train_sampler, shuffle=(train_sampler is None),
                               num_workers=2, pin_memory=True)
    val_loader    = DataLoader(val_ds, batch_size=config.batch_size,
                               shuffle=False, num_workers=2)

    optimizer = AdamW(model.parameters(), lr=config.predictor_learning_rate,
                      betas=(config.adam_beta1, config.adam_beta2),
                      weight_decay=config.adam_weight_decay)
    scheduler = OneCycleLR(optimizer, max_lr=config.predictor_learning_rate,
                           total_steps=config.epochs * len(train_loader))

    save_path = Path(config.finetuned_predictor_path)
    save_path.mkdir(parents=True, exist_ok=True)

    best_val_loss = float("inf")

    for epoch in range(config.epochs):
        if train_sampler:
            train_sampler.set_epoch(epoch)

        model.train()
        train_loss = 0.0
        for step, (x, t) in enumerate(train_loader):
            x = x.to(device)

            with torch.no_grad():
                tokens = tokenizer(x) if not distributed else tokenizer.module(x)

            lw = config.lookback_window
            input_tokens  = tokens[:, :lw]
            target_tokens = tokens[:, lw:]

            out  = model(input_tokens) if not distributed else model.module(input_tokens)
            loss = out.loss if hasattr(out, "loss") else \
                   (model.module if distributed else model).head.compute_loss(out.logits, target_tokens)

            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), config.clip)
            optimizer.step()
            scheduler.step()
            optimizer.zero_grad()

            train_loss += loss.item()

            if is_main(rank) and (step + 1) % config.log_interval == 0:
                print(f"[Predictor] Epoch {epoch+1} Step {step+1} "
                      f"loss={train_loss/(step+1):.4f}")

        # Validation
        model.eval()
        val_loss = 0.0
        with torch.no_grad():
            for x, t in val_loader:
                x = x.to(device)
                tokens = tokenizer(x) if not distributed else tokenizer.module(x)
                input_tokens  = tokens[:, :config.lookback_window]
                target_tokens = tokens[:, config.lookback_window:]
                out  = model(input_tokens) if not distributed else model.module(input_tokens)
                loss = out.loss if hasattr(out, "loss") else \
                       (model.module if distributed else model).head.compute_loss(out.logits, target_tokens)
                val_loss += loss.item()

        val_loss /= len(val_loader)
        if is_main(rank):
            print(f"[Predictor] Epoch {epoch+1} val_loss={val_loss:.4f}")
            if val_loss < best_val_loss:
                best_val_loss = val_loss
                m = model.module if distributed else model
                m.save_pretrained(str(save_path))
                print(f"  ✅ Saved best predictor (val_loss={val_loss:.4f})")

    if is_main(rank):
        summary = {"best_val_loss": best_val_loss, "epochs": config.epochs,
                   "save_path": str(save_path)}
        with open(save_path / "summary.json", "w") as f:
            json.dump(summary, f, indent=2)
        print(f"Predictor fine-tuning done. Best val_loss={best_val_loss:.4f}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", choices=["tokenizer", "predictor"], required=True)
    args = parser.parse_args()

    rank, world_size, distributed = setup_distributed()
    config = Config()

    if args.stage == "tokenizer":
        train_tokenizer(config, rank, world_size, distributed)
    else:
        train_predictor(config, rank, world_size, distributed)

    if distributed:
        dist.destroy_process_group()


if __name__ == "__main__":
    main()
