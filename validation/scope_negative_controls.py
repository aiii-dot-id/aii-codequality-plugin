#!/usr/bin/env python3
"""Mutate three release guards, require assertion failures, restore even on failure.
Run only while this source tree has no other writer or release gate.
"""
from pathlib import Path
import os
import subprocess

root = Path(__file__).resolve().parents[1]
source = root / 'main.go'
original = source.read_bytes()
go = os.environ.get('AII_GO', 'go')
env = dict(os.environ, GOTOOLCHAIN='local')
controls = [
    ('direct-byte-cap', 'if int64(len(text)) > cfg.maxFileBytes() {', 'if false {', 'TestDirectJudgeByteCap'),
    ('report-only-eligibility', 'if why := scanStepRefusal(s); why != "" {\n\t\t\tskipped = append', 'if why := scanStepRefusal(s); false {\n\t\t\tskipped = append', 'TestAdvanceSkipsReportOnlyWithoutMutation'),
    ('report-config-decoupling', 'if configErr != nil {\n\t\tcfg = config{}\n\t}', 'if configErr != nil {\n\t\treturn nil, configErr\n\t}', 'TestReportsSurviveInvalidAssessmentSettings'),
]
try:
    for name, old, new, test in controls:
        text = original.decode()
        assert text.count(old) == 1, (name, text.count(old))
        source.write_text(text.replace(old, new, 1))
        result = subprocess.run([go, 'test', '-count=1', '-run', '^'+test+'$', '.'], cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        print(name, 'exit', result.returncode, flush=True)
        print(result.stdout, flush=True)
        assert result.returncode != 0 and '--- FAIL: '+test in result.stdout and '[build failed]' not in result.stdout, 'negative control failed to fail at assertion'
        source.write_bytes(original)
finally:
    source.write_bytes(original)
subprocess.run([go, 'test', '-race', '-count=1', '-run', '^Test(DirectJudgeByteCap|AdvanceSkipsReportOnlyWithoutMutation|ReportsSurviveInvalidAssessmentSettings)$', '.'], cwd=root, env=env, check=True)
