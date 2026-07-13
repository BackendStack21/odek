#!/usr/bin/env python3
"""Export the PIGuard model from HuggingFace to ONNX.

Derived from the go-prompt-injection-guard export script, with
`trust_remote_code=True` added so it runs non-interactively in a container.
See: https://github.com/BackendStack21/go-prompt-injection-guard
"""

from pathlib import Path
import torch
from transformers import AutoModelForSequenceClassification, AutoTokenizer

MODEL_ID = "leolee99/PIGuard"
# Pinned to a specific commit for supply-chain safety.
MODEL_REVISION = "dd78b24e330193a22d2293ac66922dd4f982f563"
OUTPUT_DIR = Path.home() / ".cache" / "piguard" / "onnx"


def main():
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    onnx_path = OUTPUT_DIR / "model.onnx"

    if onnx_path.exists():
        print(f"ONNX model already exists at {onnx_path}")
        print("Delete it to re-export.")
        return

    print(f"Downloading {MODEL_ID}@{MODEL_REVISION} from HuggingFace...")
    tokenizer = AutoTokenizer.from_pretrained(
        MODEL_ID, revision=MODEL_REVISION, trust_remote_code=True
    )
    model = AutoModelForSequenceClassification.from_pretrained(
        MODEL_ID, revision=MODEL_REVISION, trust_remote_code=True
    )
    model.eval()

    # Save tokenizer (tokenizer.json is used by Rust)
    tokenizer.save_pretrained(str(OUTPUT_DIR))

    # Save config.json for label mapping
    model.config.save_pretrained(str(OUTPUT_DIR))

    # Dummy input for export
    dummy = tokenizer("Hello world", return_tensors="pt", truncation=True, max_length=512)

    print("Exporting to ONNX...")
    torch.onnx.export(
        model,
        (),
        str(onnx_path),
        input_names=["input_ids", "attention_mask"],
        output_names=["logits"],
        dynamic_axes={
            "input_ids": {0: "batch", 1: "seq"},
            "attention_mask": {0: "batch", 1: "seq"},
            "logits": {0: "batch"},
        },
        opset_version=18,
        do_constant_folding=True,
        kwargs={
            "input_ids": dummy["input_ids"],
            "attention_mask": dummy["attention_mask"],
        },
    )

    onnx_size = sum(
        f.stat().st_size for f in OUTPUT_DIR.iterdir()
        if f.suffix in (".onnx", ".data") and "model" in f.name
    )
    print(f"Done! Model saved to {OUTPUT_DIR}")
    print(f"Total size: {onnx_size / 1024 / 1024:.0f} MB")


if __name__ == "__main__":
    main()
