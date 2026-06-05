"""
One-time setup: downloads pretrained Kronos-small and Kronos-Tokenizer-base
from HuggingFace into kronos/models/ for offline use and fine-tuning.

Usage:
  python setup.py
"""

import subprocess
import sys
from pathlib import Path

MODELS_DIR = Path(__file__).parent / "models"
MODELS_DIR.mkdir(exist_ok=True)

TOKENIZER_REPO = "NeoQuasar/Kronos-Tokenizer-base"
PREDICTOR_REPO = "NeoQuasar/Kronos-base"

def download(repo_id, local_dir):
    print(f"Downloading {repo_id} -> {local_dir}")
    from huggingface_hub import snapshot_download
    snapshot_download(repo_id=repo_id, local_dir=str(local_dir))
    print("  Done")

def main():
    try:
        from huggingface_hub import snapshot_download
    except ImportError:
        subprocess.check_call([sys.executable, "-m", "pip", "install", "huggingface_hub"])
        from huggingface_hub import snapshot_download

    download(TOKENIZER_REPO, MODELS_DIR / "Kronos-Tokenizer-base")
    download(PREDICTOR_REPO, MODELS_DIR / "Kronos-base")

    # Clone Kronos source if not present
    kronos_dir = Path(__file__).parent / "Kronos"
    if not kronos_dir.exists():
        print("Cloning Kronos source...")
        subprocess.check_call(["git", "clone", "https://github.com/shiyu-coder/Kronos.git", str(kronos_dir)])
        print("  Cloned")

    print("\nSetup complete.")
    print("Next steps:")
    print("  1. python data_export.py --api-key XXXX --access-token YYYY")
    print("  2. python finetune/train.py --stage tokenizer")
    print("  3. python finetune/train.py --stage predictor")
    print("  4. python service.py")

if __name__ == "__main__":
    main()
