#!/usr/bin/env python3
"""cross-object-tenancy.yml exempts a function that CALLS a ref guard. Does the ANSWER matter?

The four `pattern-not-inside` arms release a function containing tenancy.AssertRefInWorkspace /
$G.assertRefInWorkspace / $G.assertIssueInWorkspace / $G.assertIssuesShareWorkspace. That asserts
the CALL IS PRESENT — #174 recorded the identical limit for rule C and never re-measured it here.

MUTATION per site: `if err := <guard>; err != nil {` -> `... err != nil && false {`. The call
stays, the refusal never fires, the exemption still matches.
PREDICTED: semgrep NOT CAUGHT at every site. go test is the measurement, package-scoped first and
re-asked of the WHOLE repository before any NOT CAUGHT is believed.
"""
import hashlib, os, re, subprocess, sys
REPO=os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SEMGREP=["semgrep","scan","--config",".semgrep/","--error","--metrics=off","internal/","cmd/"]
# The five that scored NOT CAUGHT at 713d22a, plus the seven that were already held (kept so the
# harness carries its own positive control: a site that was CAUGHT before must still be CAUGHT).
SITES=[("internal/notification/store.go",64,"notification"),
       ("internal/cycle/store.go",145,"cycle"),
       ("internal/label/store.go",65,"label"),
       ("internal/milestone/store.go",78,"milestone"),
       ("internal/project/store.go",58,"project"),
       ("internal/scoring/store.go",215,"scoring"),
       ("internal/featureboard/store.go",333,"featureboard"),
       ("internal/guest/store.go",251,"guest"),
       ("internal/template/store.go",388,"template"),
       ("internal/customfield/store.go",167,"customfield"),
       ("internal/dependency/store.go",264,"dependency"),
       ("internal/importer/jobs.go",67,"importer")]
def read(p): return open(os.path.join(REPO,p),encoding="utf-8").read()
def write(p,t): open(os.path.join(REPO,p),"w",encoding="utf-8").write(t)
def sha(p): return hashlib.sha256(open(os.path.join(REPO,p),"rb").read()).hexdigest()
def run(c):
    p=subprocess.run(c,cwd=REPO,capture_output=True,text=True); return p.returncode,p.stdout+p.stderr
def main():
    rows=[]
    for path,line,pkg in SITES:
        base,bsha=read(path),sha(path)
        lines=base.split("\n"); idx=line-1
        if "err != nil" not in lines[idx]:
            print(f"SKIP {path}:{line} — anchor moved: {lines[idx].strip()[:70]}"); continue
        mutated=lines[:]; mutated[idx]=lines[idx].replace("err != nil","err != nil && false",1)
        try:
            write(path,"\n".join(mutated))
            sc,_=run(SEMGREP)
            pc,po=run(["go","test","-count=1",f"./{os.path.dirname(path)}/"])
            sv="CAUGHT" if sc else "NOT CAUGHT"
            if pc: tv,scope="CAUGHT","package"
            else:
                wc,wo=run(["go","test","-timeout","300s","-count=1","./..."])
                tv,scope=("CAUGHT","whole repo") if wc else ("NOT CAUGHT","whole repo")
            print(f"{pkg:<14} semgrep={sv:<11} go-test={tv:<11} ({scope})")
            rows.append((pkg,sv,tv))
        finally:
            write(path,base); assert sha(path)==bsha,f"RESTORE FAILED {path}"
    print("\n"+"="*62)
    unheld=[r for r in rows if r[2]=="NOT CAUGHT"]
    print(f"sites measured: {len(rows)}   guard-inert NOT CAUGHT by any test: {len(unheld)}")
    for p,s,t in unheld: print(f"   UNHELD: {p}")
    print(f"semgrep caught: {sum(1 for r in rows if r[1]=='CAUGHT')} of {len(rows)}")
    print("="*62)
if __name__=="__main__": sys.exit(main())
