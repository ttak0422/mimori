#!/usr/bin/env python3
"""Isolated multi-process smoke test; prints measurements, leaves no daemon."""
import json, os, pathlib, socket, statistics, subprocess, sys, tempfile, time
binary = str(pathlib.Path(sys.argv[1] if len(sys.argv)>1 else "bin/mimori").resolve())
with tempfile.TemporaryDirectory(prefix="mm-", dir="/tmp") as state:
    def event(i, sid, kind, seq=1, **fields):
        return dict(version=1, event_id=i, provider="demo", session_id=sid,
                    generation=1, seq=seq, kind=kind,
                    observed_at="2026-10-03T16:00:00Z", **fields)
    def ingest(e):
        subprocess.run([binary,"ingest","--state-dir",state],input=json.dumps(e),text=True,check=True)
    def query(rev=None):
        with socket.socket(socket.AF_UNIX) as s:
            s.settimeout(2);s.connect(state+"/query.sock")
            s.sendall(json.dumps(dict(version=1,**({"revision":rev} if rev else {}))).encode()+b"\n")
            return json.loads(s.makefile().readline())
    def start():
        p=subprocess.Popen([binary,"daemon","--state-dir",state],stderr=subprocess.PIPE)
        for _ in range(200):
            if p.poll() is not None: raise RuntimeError(p.stderr.read().decode())
            try: query();return p
            except (OSError,ValueError): time.sleep(.01)
        p.terminate();p.wait();raise RuntimeError("startup timeout")
    root=event("root","root","idle",relation="root")
    ingest(root) # daemon absent
    for i in range(50):
        ingest(event(f"c{i}",f"c{i}","running",relation="child",parent_id="root",parent_generation=1))
    ingest(event("g","grandchild","running",relation="child",parent_id="c0",parent_generation=1))
    ingest(event("a","c0","request_open",2,request_id="a"))
    ingest(event("b","c1","request_open",2,request_id="b"))
    p=start()
    try:
        r=query();assert len(r["roots"])==1 and r["roots"][0]["unresolved_requests"]==2
        assert r["roots"][0]["running_descendants"]==51
        ingest(event("answer","c0","request_resolved",3,request_id="a"))
        for _ in range(100):
            r=query()
            if r["roots"][0]["unresolved_requests"]==1:break
            time.sleep(.01)
        assert r["roots"][0]["unresolved_requests"]==1
        rev=r["revision"];ingest(root);time.sleep(.2);assert query(rev)["unchanged"]
        times=[]
        for _ in range(200):
            before=time.perf_counter();u=query(rev);times.append((time.perf_counter()-before)*1000)
            assert u["unchanged"]
        cli=[]
        for _ in range(30):
            before=time.perf_counter();subprocess.run([binary,"query","--state-dir",state,"--revision",rev],check=True,stdout=subprocess.DEVNULL);cli.append((time.perf_counter()-before)*1000)
        ensures=[]
        for _ in range(30):
            before=time.perf_counter()
            ready=json.loads(subprocess.check_output([binary,"ensure","--state-dir",state]))
            ensures.append((time.perf_counter()-before)*1000)
            assert ready==dict(version=1,ready=True)
        assert query()["revision"]==rev
        p.kill();_,status,cpu=os.wait4(p.pid,0);p.returncode=os.waitstatus_to_exitcode(status);p=start();after=query(rev)
        assert not after.get("unchanged") and after["roots"][0]["unresolved_requests"]==1
        print(json.dumps(dict(sessions=52,socket_samples=len(times),socket_median_ms=statistics.median(times),socket_p95_ms=sorted(times)[189],cli_samples=len(cli),cli_median_ms=statistics.median(cli),cli_p95_ms=sorted(cli)[28],ensure_reuse_median_ms=statistics.median(ensures),ensure_reuse_p95_ms=sorted(ensures)[28],unchanged_bytes=len(json.dumps(u,separators=(",",":")))+1,snapshot_bytes=len(json.dumps(r,separators=(",",":")))+1,daemon_cpu_seconds=cpu.ru_utime+cpu.ru_stime,crash_recovery=True),indent=2))
    finally:
        p.terminate();p.wait(timeout=5)
