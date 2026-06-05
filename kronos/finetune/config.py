"""
NSE fine-tuning config — mirrors Kronos finetune/config.py structure
but uses our Kite-exported pickle files instead of Qlib.
"""

from pathlib import Path

BASE = Path(__file__).parent.parent

class Config:
    # ── Data ──────────────────────────────────────────────────────────────
    dataset_path        = str(BASE / "data")
    feature_list        = ["open", "high", "low", "close", "vol", "amt"]
    time_feature_list   = ["weekday", "day", "month"]
    lookback_window     = 90
    predict_window      = 5
    max_context         = 512

    train_time_range    = ["2019-01-01", "2023-12-31"]
    val_time_range      = ["2023-01-01", "2024-12-31"]
    test_time_range     = ["2025-01-01", "2026-06-06"]

    # ── Pretrained model paths ─────────────────────────────────────────────
    pretrained_tokenizer_path = str(BASE / "models" / "Kronos-Tokenizer-base")
    pretrained_predictor_path = str(BASE / "models" / "Kronos-small")

    # ── Output ────────────────────────────────────────────────────────────
    save_path                    = str(BASE / "models")
    tokenizer_save_folder_name   = "nse_tokenizer"
    predictor_save_folder_name   = "nse_predictor"

    # ── Training hyperparams ──────────────────────────────────────────────
    seed                  = 42
    epochs                = 20
    batch_size            = 32
    n_train_iter          = 50_000
    n_val_iter            = 10_000
    tokenizer_learning_rate = 2e-4
    predictor_learning_rate = 4e-5
    accumulation_steps    = 1
    clip                  = 5.0
    adam_beta1            = 0.9
    adam_beta2            = 0.95
    adam_weight_decay     = 0.1
    log_interval          = 100

    # ── Inference ────────────────────────────────────────────────────────
    inference_T            = 0.6
    inference_top_p        = 0.9
    inference_top_k        = 0
    inference_sample_count = 5

    # Derived paths
    @property
    def finetuned_tokenizer_path(self):
        return str(Path(self.save_path) / self.tokenizer_save_folder_name)

    @property
    def finetuned_predictor_path(self):
        return str(Path(self.save_path) / self.predictor_save_folder_name)
