"""Generate and reread the deterministic 1/9-record A12 Alpaca fixtures.

Each raw object is exactly 30 MiB; no dataset is held in memory. Verification
holds one bounded object and records actual source/object hashes and sizes.
Generated datasets belong outside Git and are not training acceptance proof.
"""

import argparse
import hashlib
import json
from pathlib import Path

OBJECT_BYTES = 30 * 1024 * 1024
MINIMUM_TEXT_BYTES = 29 * 1024 * 1024
TEXT = b"Ordinary deterministic training text. "
CHUNK = (TEXT * (65536 // len(TEXT) + 1))[:65536]


def unique_fields(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate Alpaca field: {key}")
        result[key] = value
    return result


def write_record(stream, ordinal):
    prefix = f'{{"instruction":"Record {ordinal:08d}. '.encode()
    suffix = f'","input":"","output":"Record {ordinal:08d}: complete."}}'.encode()
    stream.write(prefix)
    remaining = OBJECT_BYTES - len(prefix) - len(suffix)
    while remaining:
        count = min(remaining, len(CHUNK))
        stream.write(CHUNK[:count])
        remaining -= count
    stream.write(suffix)


def verify(path, count):
    digest = hashlib.sha256()
    size = 0

    def consume(stream, length):
        nonlocal size
        raw = stream.read(length)
        if len(raw) != length:
            raise ValueError("truncated Alpaca fixture")
        size += len(raw)
        digest.update(raw)
        return raw

    records = []
    with path.open("rb") as stream:
        if consume(stream, 1) != b"[":
            raise ValueError("fixture is not an Alpaca array")
        for ordinal in range(1, count + 1):
            if ordinal > 1 and consume(stream, 1) != b",":
                raise ValueError("invalid Alpaca array separator")
            offset = size
            raw = consume(stream, OBJECT_BYTES)
            row = json.loads(raw, object_pairs_hook=unique_fields)
            if (not isinstance(row, dict) or set(row) != {"instruction", "input", "output"}
                or any(not isinstance(value, str) for value in row.values())
                or not row["instruction"].startswith(f"Record {ordinal:08d}. ")
                or row["input"] != ""
                or row["output"] != f"Record {ordinal:08d}: complete."):
                raise ValueError(f"record {ordinal} does not have the strict unique Alpaca content")
            text_bytes = sum(len(value.encode("utf-8")) for value in row.values())
            if text_bytes < MINIMUM_TEXT_BYTES:
                raise ValueError(f"record {ordinal} has insufficient decoded text")
            records.append({
                "ordinal": ordinal, "source_byte_offset": str(offset),
                "object_size_bytes": str(len(raw)),
                "object_sha256": hashlib.sha256(raw).hexdigest(),
                "decoded_text_bytes": str(text_bytes),
            })
        if consume(stream, 1) != b"]" or stream.read(1):
            raise ValueError("fixture has trailing or malformed array content")
    return {
        "schema_version": "ani.modeldev.long-alpaca-fixture.v1",
        "source_format": "JSON", "row_count": str(count),
        "source_size_bytes": str(size), "source_sha256": digest.hexdigest(),
        "records": records,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--records", type=int, choices=(1, 9), required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--facts", type=Path, required=True)
    args = parser.parse_args()
    if args.output == args.facts or args.facts.exists():
        parser.error("source and new facts must be distinct exclusive paths")
    with args.output.open("xb") as stream:
        stream.write(b"[")
        for ordinal in range(1, args.records + 1):
            if ordinal > 1:
                stream.write(b",")
            write_record(stream, ordinal)
        stream.write(b"]")
    facts = verify(args.output, args.records)
    facts["generator_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    with args.facts.open("x", encoding="utf-8") as stream:
        json.dump(facts, stream, sort_keys=True, indent=2)
        stream.write("\n")
    print(json.dumps({"source": str(args.output), "facts": str(args.facts),
                      "source_size_bytes": facts["source_size_bytes"],
                      "source_sha256": facts["source_sha256"],
                      "row_count": facts["row_count"]}, sort_keys=True))


if __name__ == "__main__":
    main()
