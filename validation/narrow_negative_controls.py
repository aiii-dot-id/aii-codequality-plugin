"""Run from repository root; mutate/restores only source guards, never WASM."""
from pathlib import Path
import os
import subprocess
os.environ['GOTOOLCHAIN'] = 'local'
cases = [
 ('config.go', 'maximumFileBytes    = 192 << 10', 'maximumFileBytes    = 1 << 20', 'TestNarrowReleaseFileCap'),
 ('main.go', 'if s.MaxFileBytes <= 0 || s.MaxFileBytes > maximumFileBytes {', 'if false && (s.MaxFileBytes <= 0 || s.MaxFileBytes > maximumFileBytes) {', 'TestExperimentalScanIsReportOnlyWithoutMutation'),
]
for name, old, new, test in cases:
 p = Path(name); saved = p.read_text()
 assert saved.count(old) == 1, (name, old)
 try:
  p.write_text(saved.replace(old, new))
  r = subprocess.run([os.environ.get('AII_GO', 'go'), 'test', '-count=1', '-run', '^'+test+'$', '.'], text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
  print(name, test, r.returncode, r.stdout, flush=True)
  assert r.returncode != 0 and '--- FAIL:' in r.stdout and '[build failed]' not in r.stdout, 'guard mutation failed to reach test failure'
 finally:
  p.write_text(saved)
r = subprocess.run([os.environ.get('AII_GO', 'go'), 'test', '-race', '-count=1', '-run', 'TestNarrowReleaseFileCap|TestExperimentalScanIsReportOnlyWithoutMutation', '.'])
raise SystemExit(r.returncode)
