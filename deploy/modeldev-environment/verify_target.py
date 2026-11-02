#!/usr/bin/env python3
"""Read all designated cluster identity fields through its sole approved SSH entry.

Only the resource config's network.cluster_id is emitted. Secret bytes and all
other configuration remain in the remote process and are never saved/logged.
"""
import argparse
import json
from pathlib import Path
import subprocess
from publisher import TARGET, canonical

REMOTE = r'''
import base64,json,subprocess,time,yaml

def kubectl(*args):
    return subprocess.run(["kubectl","--request-timeout=5s",*args],check=True,capture_output=True,text=True).stdout
config=json.loads(kubectl("config","view","--minify","-o","json"))
context=kubectl("config","current-context").strip()
apiserver=config["clusters"][0]["cluster"]["server"]
kube_system_uid=json.loads(kubectl("get","namespace","kube-system","-o","json"))["metadata"]["uid"]
secret=json.loads(kubectl("-n","ani-system","get","secret","ani-resource-private","-o","json"))
config_bytes=base64.b64decode(secret["data"]["config.yaml"],validate=True)
cluster_id=yaml.safe_load(config_bytes)["network"]["cluster_id"]
print(json.dumps({"ssh_user":"kubercloud","ssh_host":"100.64.128.12","context":context,"apiserver":apiserver,"cluster_id":cluster_id,"kube_system_uid":kube_system_uid,"verified_at_unix":time.time()},separators=(",",":")))
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    result = subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "kubercloud@100.64.128.12", "python3 -"], input=REMOTE, text=True, capture_output=True, timeout=35)
    if result.returncode:
        raise RuntimeError("designated cluster identity read failed; no writes authorized by this receipt")
    identity = json.loads(result.stdout)
    if set(identity) != set(TARGET) | {"verified_at_unix"} or any(identity.get(key) != value for key, value in TARGET.items()):
        raise RuntimeError("complete designated cluster identity mismatch; no receipt written")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    temporary = args.output.with_suffix(".next")
    temporary.write_bytes(canonical(identity))
    temporary.replace(args.output)


if __name__ == "__main__":
    main()
