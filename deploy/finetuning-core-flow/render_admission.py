#!/usr/bin/env python3
"""Compatibility path to the formal renderer; no task hook or second policy.

Uses formal renderer's explicit config/image/issuer/namespace arguments. Original
task manifests and the user's rejected registration remain historical evidence.
"""
from pathlib import Path
import runpy

if __name__ == "__main__":
    runpy.run_path(str(Path(__file__).resolve().parents[1] / "modeldev-webhook/render.py"), run_name="__main__")
