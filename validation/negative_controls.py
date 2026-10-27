from pathlib import Path
import subprocess,os
os.environ['GOTOOLCHAIN']='local'
cases=[
 ('cq/pipeline.go','!ok || gate.CacheEligible()','!ok || gate.CacheEligible() || true','TestScanFreshAliasBypassesCacheThenUsesConcreteModel','.'),
 ('model_pin.go','if p.Model != "" && actual != p.Model {','if false && p.Model != "" && actual != p.Model {','TestScanPinPersistsBeforeAnswerAndRejectsDrift','.'),
 ('scan_judge.go','if !j.runtime.Retry.Ready(j.nowMS) {','if false && !j.runtime.Retry.Ready(j.nowMS) {','TestScanBackoffSurvivesRestartAndStopsBeforeSend','.'),
]
for name,old,new,test,pkg in cases:
 p=Path(name); saved=p.read_text();assert old in saved
 try:
  p.write_text(saved.replace(old,new))
  r=subprocess.run([os.environ.get('AII_GO', 'go'),'test','-count=1','-run','^'+test+'$',pkg],text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
  print(name,test,r.returncode,r.stdout,flush=True)
  assert r.returncode!=0 and '--- FAIL:' in r.stdout and '[build failed]' not in r.stdout,'negative control did not reach assertion'
 finally:p.write_text(saved)
r=subprocess.run([os.environ.get('AII_GO', 'go'),'test','-race','-count=1','-run','TestScanPin|TestScanFresh|TestScanBackoff|TestJournalWriter','.','./cq'])
raise SystemExit(r.returncode)
