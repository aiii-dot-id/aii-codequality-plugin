#!/usr/bin/env python3
import hashlib
import json
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parent.parent
names = subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=root).decode().split('\0')
files = {}
for name in sorted(set(filter(None, names))):
    path = root / name
    files[name] = hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else 'ABSENT'
encoded = json.dumps(files, sort_keys=True, separators=(',', ':')).encode()
state = {'head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root).decode().strip(),
         'source_sha256': hashlib.sha256(encoded).hexdigest(), 'files': files,
         'wasm_sha256': hashlib.sha256((root / 'dist/id.aiii.codequality.wasm').read_bytes()).hexdigest()}
target = root / 'dist/candidate-freeze.json'
if '--check' in sys.argv:
    old = json.loads(target.read_text())
    if state != old:
        changed = sorted(k for k in set(files) | set(old['files']) if files.get(k) != old['files'].get(k))
        raise SystemExit('FREEZE DRIFT: ' + repr(changed) + '; head/artifact also compared')
    print('FREEZE UNCHANGED', state['source_sha256'], state['wasm_sha256'])
else:
    target.write_text(json.dumps(state, indent=2, sort_keys=True) + '\n')
    print('FROZEN', len(files), 'files', state['source_sha256'], state['wasm_sha256'])
