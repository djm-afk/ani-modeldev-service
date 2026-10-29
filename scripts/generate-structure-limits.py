#!/usr/bin/env python3
"""Generate/check Python structure guards from the current public Go contract."""
import argparse
import ast
import json
from pathlib import Path
import re


def integer(node):
    if isinstance(node, ast.Constant) and isinstance(node.value, int):
        return node.value
    if isinstance(node, ast.BinOp) and isinstance(node.op, (ast.Add, ast.Mult)):
        left, right = integer(node.left), integer(node.right)
        return left + right if isinstance(node.op, ast.Add) else left * right
    raise ValueError("structure guard must be a literal integer expression")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    source = (root / "contract/cpup01/content_limits.go").read_text()
    fields = re.findall(r"^\s*(Max[A-Za-z]+)\s*=\s*([^\n]+)$", source, flags=re.MULTILINE)
    values = {name: integer(ast.parse(expression.strip(), mode="eval").body) for name, expression in fields}
    if len(values) != 14 or any(value <= 0 for value in values.values()):
        raise ValueError("complete current structure guard definition required")
    raw = (json.dumps(values, sort_keys=True, indent=2) + "\n").encode()
    destination = root / "training/structure_limits.json"
    if args.check:
        if destination.read_bytes() != raw:
            raise ValueError("generated structure guards differ")
    else:
        destination.write_bytes(raw)
    implementation = root / "training/implementations.json"
    original = (root / "contract/cpup01/implementations.json").read_bytes()
    if args.check:
        if implementation.read_bytes() != original:
            raise ValueError("generated Python implementation definition differs")
    else:
        implementation.write_bytes(original)


if __name__ == "__main__":
    main()
