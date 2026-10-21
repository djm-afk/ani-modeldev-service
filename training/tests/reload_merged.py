"""Load detached HF bytes independently, without importing workload code."""

import hashlib
import json
from pathlib import Path
import sys

import torch
from transformers import AutoModelForCausalLM, AutoTokenizer

root = Path(sys.argv[1])
torch.set_num_threads(2)
tokenizer = AutoTokenizer.from_pretrained(
    root, local_files_only=True, trust_remote_code=False
)
model = AutoModelForCausalLM.from_pretrained(
    root,
    torch_dtype=torch.float32,
    local_files_only=True,
    trust_remote_code=False,
    attn_implementation="eager",
).eval()
encoded = tokenizer("What is two plus two?", return_tensors="pt")
with torch.inference_mode():
    logits = model(**encoded).logits
    if not torch.isfinite(logits).all():
        raise ValueError("detached model returned nonfinite logits")
    result = model.generate(**encoded, max_new_tokens=8, do_sample=False)
print(
    json.dumps(
        {
            "device": str(model.device),
            "dtype": str(model.dtype),
            "finite_logits": True,
            "generated_token_ids": result[0, encoded["input_ids"].shape[-1] :].tolist(),
            "files": [
                {
                    "path": p.name,
                    "bytes": p.stat().st_size,
                    "sha256": hashlib.sha256(p.read_bytes()).hexdigest(),
                }
                for p in sorted(root.iterdir())
                if p.is_file()
            ],
        }
    )
)
