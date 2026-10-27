//go:build experimental

package guest_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestDiagnosticOneMiBMemory(t *testing.T) {
	startAtMemory := startDiagnostic
	artifact := os.Getenv("GUEST_DIAGNOSTIC_WASM")
	if artifact == "" {
		t.Skip("diagnostic only: set GUEST_DIAGNOSTIC_WASM to an unsigned fixture artifact")
	}
	data, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	t.Logf("diagnostic artifact=%s sha256=%s bytes=%d", artifact, hash, len(data))
	for _, ceiling := range []int{64 << 20, 128 << 20} {
		for _, restartEvery := range []int{1, 3} {
			t.Run(fmt.Sprintf("MiB%d_restart%d", ceiling>>20, restartEvery), func(t *testing.T) {
				b := newBroker()
				b.failDocs = false
				b.source = largeSource()
				t.Logf("fixture bytes=%d sha256=%x ceiling=%d", len(b.source), sha256.Sum256([]byte(b.source)), ceiling)
				w := startAtMemory(t, b, hash, ceiling, artifact)
				steps, restarts := 0, 0
				t.Cleanup(func() {
					t.Logf("diagnostic progress steps=%d restarts=%d source_reads=%d http_calls=%d max_body=%d max_params=%d max_frame=%d max_result=%d ceiling=%d", steps, restarts, b.sourceReads, b.methods["http.post"], b.maxBody, b.maxParams, w.maxFrame, w.maxResult, ceiling)
				})
				w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": "guest_fixture"})
				done := false
				for steps < 160 {
					steps++
					r := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": "guest_fixture"}))
					report(t, w, "review_queue")
					report(t, w, "coverage")
					t.Logf("step=%d status=%v chunks=%v calls=%v source_reads=%d", steps, r["status"], r["chunks"], r["calls"], b.sourceReads)
					if r["status"] == "done" {
						if r["files_judged"] != float64(1) || r["chunks"].(float64) < 2 || b.sourceReads < 16 {
							t.Fatalf("not assessed: %s", raw(r))
						}
						done = true
						break
					}
					if r["status"] != "running" && r["status"] != "judge_unavailable" {
						t.Fatalf("scan stopped: %s", raw(r))
					}
					if steps%restartEvery == 0 {
						w.close()
						w = startAtMemory(t, b, hash, ceiling, artifact)
						restarts++
					}
				}
				if !done || steps < 2 {
					t.Fatalf("not finished: steps=%d done=%t", steps, done)
				}
				baseline := string(raw(report(t, w, "review_queue")))
				coverage := string(raw(report(t, w, "coverage")))
				for i := 0; i < 20; i++ {
					if string(raw(report(t, w, "review_queue"))) != baseline || string(raw(report(t, w, "coverage"))) != coverage {
						t.Fatal("report drift")
					}
				}
				if b.maxEscaping < 1000 || b.maxBody > bodyLimit {
					t.Fatalf("request/escape exercise failed: %d %d", b.maxEscaping, b.maxBody)
				}
				w.close()
			})
		}
	}
}

func startDiagnostic(t *testing.T, b *broker, hash string, ceiling int, artifact string) *worker {
	t.Helper()
	if os.Getenv("GUEST_DIAGNOSTIC_NODE") != "1" {
		return startAtMemory(t, b, hash, ceiling, artifact)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	t.Cleanup(cancel)
	w := &worker{t: t, b: b}
	w.cmd = exec.CommandContext(ctx, "node", "-e", diagnosticNode, artifact, fmt.Sprint(ceiling), hash)
	var err error
	w.in, err = w.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	w.out, err = w.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	w.cmd.Stderr = &w.stderr
	if err = w.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !w.closed {
			w.close()
		}
		t.Log(w.stderr.text())
	})
	return w
}

const diagnosticNode = `
const fs=require('fs'), crypto=require('crypto');
const original=fs.readFileSync(process.argv[1]), cap=Number(process.argv[2]);
if(crypto.createHash('sha256').update(original).digest('hex')!==process.argv[3]) throw Error('hash mismatch');
function leb(n){const a=[];do{let b=n&127;n>>>=7;a.push(b|(n?128:0))}while(n);return Buffer.from(a)}
let pos=8;
function readleb(){let v=0,s=0,b;do{b=original[pos++];v|=(b&127)<<s;s+=7}while(b&128);return v}
const sections=[original.subarray(0,8)];
while(pos<original.length){const begin=pos,id=original[pos++],size=readleb(),end=pos+size;
 if(id===5){const count=readleb(),flags=readleb(),min=readleb();if(count!==1||flags>1||min>cap/65536)throw Error('unsupported memory');
 const payload=Buffer.concat([leb(1),leb(1),leb(min),leb(cap/65536)]);sections.push(Buffer.concat([Buffer.from([5]),leb(payload.length),payload]));
 }else sections.push(original.subarray(begin,end));pos=end;
}
function readN(n){let b=Buffer.alloc(n),p=0;while(p<n){const k=fs.readSync(0,b,p,n-p,null);if(!k){if(!p)return null;throw Error('short frame')}p+=k}return b}
function read(){const h=readN(4);if(!h)return null;const n=h.readUInt32BE();if(n>1048576)throw Error('frame cap');return JSON.parse(readN(n))}
function write(v){const b=Buffer.from(JSON.stringify(v)),h=Buffer.alloc(4);h.writeUInt32BE(b.length);fs.writeSync(1,h);fs.writeSync(1,b)}
let ex,seq=0,last=0;
function memory(where){const n=ex.memory.buffer.byteLength;if(n!==last||where==='trap'){console.error('NODE memory_bytes='+n+' ceiling='+cap+' where='+where);last=n}}
function store(b){const p=ex.cabi_realloc(0,0,1,b.length);new Uint8Array(ex.memory.buffer,p,b.length).set(b);return p}
const imports={'aiii:bbb/bbb':{'invoke-call':(p,n,r)=>{
 memory('hostcall');const params=JSON.parse(Buffer.from(ex.memory.buffer,p,n).toString());const id='w'+(++seq);
 write({jsonrpc:'2.0',id,method:'invoke.call',params});const res=read();if(res.id!==id)throw Error('reply id');
 const b=Buffer.from(JSON.stringify(res.result===undefined?res.error:res.result)),q=store(b);
 const dv=new DataView(ex.memory.buffer);dv.setUint32(r,q,true);dv.setUint32(r+4,b.length,true);
}}};
try{
 ex=new WebAssembly.Instance(new WebAssembly.Module(Buffer.concat(sections)),imports).exports;
 ex._initialize();if(ex['aiii-plugin-bbb-protocol-version']()!==2||ex['aiii-plugin-smoke']()!==1)throw Error('admission');memory('ready');
 for(;;){const req=read();if(req===null)break;const b=Buffer.from(JSON.stringify(req)),p=store(b),r=ex['plugin-invoke'](p,b.length),dv=new DataView(ex.memory.buffer);
 const out=JSON.parse(Buffer.from(ex.memory.buffer,dv.getUint32(r,true),dv.getUint32(r+4,true)).toString());
 ex['cabi_post_plugin-invoke'](r);memory('return');write(out)}
}catch(e){if(ex)memory('trap');console.error(e.stack);process.exitCode=3}
`
