#!/usr/bin/env python3
"""Seal a document directory. Does not sign, approve, or run any business work."""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path


def digest(path: Path) -> str:
    with path.open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    root = args.root.resolve()
    for name in ('review/document-checks.json', 'review/browser-checks.json'):
        report = root / name
        if report.exists():
            data = json.loads(report.read_text(encoding='utf-8'))
            if data.get('scope') != 'DOCUMENT_ONLY':
                raise SystemExit(f'Refuse non-document review status: {name}')
    paths = sorted(p for p in root.rglob('*') if p.is_file()
                   and '__pycache__' not in p.parts and p.suffix != '.pyc'
                   and p.relative_to(root).as_posix() not in ('manifest.json', 'SHA256SUMS'))
    for path in paths:
        if path.is_symlink():
            raise SystemExit(f'Refuse symlink: {path}')
    records = [{'path': p.relative_to(root).as_posix(), 'size_bytes': p.stat().st_size, 'sha256': digest(p)} for p in paths]
    manifest = {'scope': 'DOCUMENT_PACKAGE_INTEGRITY_ONLY', 'document_version': 'v4.2.2',
                'spec_sha256': digest(root / 'spec.md'),
                'not_an_execution_or_release_approval': True,
                'excluded_from_payload': ['manifest.json', 'SHA256SUMS', '**/__pycache__/**', '**/*.pyc'],
                'payload_count': len(records), 'files': records}
    manifest_path = root / 'manifest.json'
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    all_paths = sorted(paths + [manifest_path])
    (root / 'SHA256SUMS').write_text(''.join(f'{digest(p)}  {p.relative_to(root).as_posix()}\n' for p in all_paths), encoding='utf-8')
    print(json.dumps({'payload_count': len(records), 'checksum_entries': len(all_paths), 'spec_sha256': manifest['spec_sha256']}, ensure_ascii=False))

if __name__ == '__main__':
    main()
