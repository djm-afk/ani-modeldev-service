"""Read the image-owned implementation contract also embedded by Go."""

import json
from pathlib import Path


def implementation(identifier):
    source = Path(__file__).resolve().with_name("implementations.json")
    contract = json.loads(source.read_bytes())
    return contract["implementations"][identifier]


def defaults(identifier):
    return dict(implementation(identifier)["defaults"])
