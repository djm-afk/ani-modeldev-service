import datetime,hashlib,json,os,pathlib,subprocess,sys
base=pathlib.Path("/home/chabking/workspace/ani-governance-runs/modeldev-create-maintenance-20261101")
source=base/"source";os.chdir(source)
env=os.environ.copy();env.update(PATH="/home/chabking/workspace/ani-governance-runs/modeldev-tenant-env-20261101/tools:"+env["PATH"],GOTOOLCHAIN="auto",GOMAXPROCS="2",GOFLAGS="-p=2")
files=["app/admin/service/internal/data/modeldev_maintenance_config.go","app/admin/service/internal/data/modeldev_maintenance_config_test.go","app/admin/service/internal/data/platform_component_log_scope_test.go","app/admin/service/internal/service/modeldev_create_maintenance_test.go","docs/contracts/frontend-http-api.md","docs/contracts/permission-code-capability-map.tsv","docs/contracts/service-to-service-calls.md","pkg/permcode/capability_gen.go","pkg/permcode/permcode_test.go","sql/bootstrap/catalog_test.go"]
assert hashlib.sha256(pathlib.Path("go.mod").read_bytes()).hexdigest()=="eccba55c294e83ea62420f2ff5a424f754b688f6dec8bee95ba25d6770e6883b"
assert hashlib.sha256(pathlib.Path("go.sum").read_bytes()).hexdigest()=="898509f4d5b534b6d02c4220dc7fbf64bd3643433e9ffc6316cb8eca429dfa94"
assert not pathlib.Path("go.work").exists()
subprocess.run(["gofmt","-w"]+[p for p in files if p.endswith(".go") and not p.endswith("capability_gen.go")],env=env,check=True)
commands=[("generate",["python3","scripts/permcode/gen-go-maps.py"]),("generated-check",["python3","scripts/permcode/gen-go-maps.py","--check"]),("targeted",["go","test","-race","-count=1","-v","-timeout=3m","-run","^(TestModelDevCreateMaintenance|TestPlatformComponentLogScopeRejectsUnregistered)","./app/admin/service/internal/data","./app/admin/service/internal/service"]),("make-check",["make","check"])]
for name,command in commands:
 stage="maintenance-default-pause-"+name;start=datetime.datetime.now(datetime.timezone.utc).isoformat();log=base/(stage+".log")
 with log.open("w") as output:result=subprocess.run(command,env=env,stdout=output,stderr=subprocess.STDOUT)
 receipt={"started_at":start,"completed_at":datetime.datetime.now(datetime.timezone.utc).isoformat(),"command":command,"cwd":str(source),"execution_host":"ssh fedora","source_base":"6a7c721ea113d51497421d7975122ec7ca8105c2","exit_code":result.returncode,"source_sha256":{p:hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest() for p in files},"dependency_sha256":{p:hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest() for p in ["go.mod","go.sum"]},"environment":{"GOTOOLCHAIN":"auto","GOMAXPROCS":"2","GOFLAGS":"-p=2"},"scope":"dedicated old-API maintenance candidate; default paused, explicit false resumes; no dynamic environment API/pin migration; no PG lease rerun"}
 (base/(stage+"-receipt.json")).write_text(json.dumps(receipt,indent=2)+"\n");print(json.dumps(receipt),flush=True);print(log.read_text()[-10000:],flush=True)
 if result.returncode:sys.exit(result.returncode)
