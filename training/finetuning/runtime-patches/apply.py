"""Apply the one pinned build-time Arrow writer patch; no version selectors."""
import hashlib
import json
from pathlib import Path
import re
import sys

root = Path(__file__).resolve().parent
receipt = json.loads((root / 'official-source.json').read_bytes())
if len(receipt['patches']) != 1:
    raise RuntimeError('only the fixed Arrow writer patch is authorized')
entry = receipt['patches'][0]
if entry['path'] != 'align-writer-batch-size.patch' or len(entry['files']) != 1:
    raise RuntimeError('unexpected upstream patch scope')
patch = (root / entry['path']).read_bytes()
if hashlib.sha256(patch).hexdigest() != entry['sha256']:
    raise RuntimeError('Arrow patch SHA256 differs')
file = entry['files'][0]
if file['path'] != 'src/llamafactory/data/converter.py':
    raise RuntimeError('unexpected upstream patch target')
if len(sys.argv) != 2:
    raise RuntimeError('usage: apply.py <exact official source root>')
path = Path(sys.argv[1]) / file['path']
original = path.read_bytes()
if hashlib.sha256(original).hexdigest() != file['before_sha256']:
    raise RuntimeError('official converter source SHA256 differs')
lines = patch.splitlines(keepends=True)
if lines[:2] != [b'--- a/src/llamafactory/data/converter.py\n', b'+++ b/src/llamafactory/data/converter.py\n']:
    raise RuntimeError('unexpected patch paths')
match = re.fullmatch(rb'@@ -(\d+),(\d+) \+(\d+),(\d+) @@\n', lines[2])
if not match or int(match[4]) != int(match[2]) + 1:
    raise RuntimeError('expected a single one-line hunk')
old, new, additions = [], [], []
for line in lines[3:]:
    if line.startswith(b' '):
        old.append(line[1:]); new.append(line[1:])
    elif line.startswith(b'+'):
        new.append(line[1:]); additions.append(line[1:])
    else:
        raise RuntimeError('upstream removals or extra hunks are forbidden')
if additions != [b'            writer_batch_size=1,\n']:
    raise RuntimeError('only writer_batch_size=1 may be added')
source = original.splitlines(keepends=True)
start = int(match[1]) - 1
if source[start:start + int(match[2])] != old:
    raise RuntimeError('exact converter hunk differs')
changed = b''.join(source[:start] + new + source[start + len(old):])
if hashlib.sha256(changed).hexdigest() != file['after_sha256']:
    raise RuntimeError('patched converter SHA256 differs')
path.write_bytes(changed)
print(json.dumps({'state': 'EXACT_ARROW_WRITER_PATCH_APPLIED', 'patch_sha256': entry['sha256'],
                  'before_sha256': file['before_sha256'], 'after_sha256': file['after_sha256'],
                  'target': str(path)}, sort_keys=True))
