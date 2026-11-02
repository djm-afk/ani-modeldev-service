import datetime, hashlib, json, os, pathlib, subprocess, sys
base=pathlib.Path("/home/chabking/workspace/ani-governance-runs/modeldev-tenant-env-20261101")
source=base/"source"
os.chdir(source)
env=os.environ.copy()
env.update(PATH=str(base/"tools")+":"+env["PATH"], GOTOOLCHAIN="auto", GOMAXPROCS="2", GOFLAGS="-p=2")
relative="app/admin/service/internal/data/platform_component_log_scope_test.go"
subprocess.run(["gofmt","-w",relative],env=env,check=True)
source_hash=hashlib.sha256(pathlib.Path(relative).read_bytes()).hexdigest()
if source_hash!="09df6e331be2b29939448147ed37f3336b9f3cdf9901f96f1ecd3ae701c7880a":
    raise SystemExit("source identity differs after gofmt")
receipts=[]
def run(stage,args):
    started=datetime.datetime.now(datetime.timezone.utc).isoformat()
    logfile=base/(stage+".log")
    with logfile.open("w") as output:
        result=subprocess.run(args,env=env,stdout=output,stderr=subprocess.STDOUT)
    completed=datetime.datetime.now(datetime.timezone.utc).isoformat()
    receipt={"stage":stage,"started_at":started,"completed_at":completed,"command":args,"cwd":str(source),"execution_host":"ssh fedora","exit_code":result.returncode,"source_base":"5cfa9065190b9585b799e428537a0019ca70e41a","source_file":relative,"source_sha256":source_hash,"environment":{"GOTOOLCHAIN":"auto","GOMAXPROCS":"2","GOFLAGS":"-p=2"}}
    (base/(stage+"-receipt.json")).write_text(json.dumps(receipt,indent=2)+"\n")
    receipts.append(receipt)
    print(json.dumps(receipt),flush=True)
    print(logfile.read_text(),flush=True)
    if result.returncode:
        sys.exit(result.returncode)
    return logfile.read_text()
identity=run("gitleaks-fixture-scanner-identity",["go","version","-m",str(base/"tools/gitleaks")])
if "github.com/zricethezav/gitleaks/v8" not in identity or "v8.30.1" not in identity:
    raise SystemExit("unexpected pinned scanner identity")
run("gitleaks-fixture-scanner-sha256",["sha256sum",str(base/"tools/gitleaks"),str(base/"tools/govulncheck")])
run("gitleaks-fixture-target-test",["go","test","-race","-count=1","-v","-timeout=2m","-run","^TestPlatformComponentLogScopeRejectsUnregistered$","./app/admin/service/internal/data"])
run("gitleaks-fixture-target-scan",[str(base/"tools/gitleaks"),"dir","--no-banner","--no-color","--redact","."])
run("gitleaks-fixture-required-audit",["bash","scripts/ci/audit.sh"])
