#!/usr/bin/env python3
"""Compile four fixed IRs and assemble one immutable public material bundle."""
import argparse
import json
from pathlib import Path
import re
import subprocess
import sys
from publisher import PROFILES, canonical, ir_struct, read_json, sha, uuid_id

PROFILE_CONTRACT = {"modeldev-mlp-cpu": ("builtin.mlp", "cpu"), "modeldev-mlp-gpu": ("builtin.mlp", "gpu"), "modeldev-sft-lora-cpu": ("builtin.sft_lora", "cpu"), "modeldev-sft-lora-gpu": ("builtin.sft_lora", "gpu")}
PINNED_KFP_SHA = "85a38e0719870e5b2f97a74f5c966d79bcc7c960"


def immutable(path, raw):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("xb") as output:
        output.write(raw)
    path.chmod(0o444)


def image_authority(reference, authority, project, anonymous):
    if not isinstance(reference, str) or not re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", reference):
        raise ValueError("all fixed images require digest references")
    if reference.startswith(authority + "/" + project + "/"):
        return
    if reference not in anonymous:
        raise ValueError("MANAGED_IMAGE_REGISTRY_MISMATCH")


def prepare(config, destination, compiler):
    if set(config) != {"source_commit", "source_tree", "registry_authority", "platform_project", "anonymous_images", "support_images", "profiles"} or not re.fullmatch(r"[0-9a-f]{40}", config["source_commit"]) or not re.fullmatch(r"[0-9a-f]{40}", config["source_tree"]):
        raise ValueError("explicit source commit/tree and complete fixed build inputs required")
    authority, project = config["registry_authority"], config["platform_project"]
    if not authority or "/" in authority or not re.fullmatch(r"[a-z0-9][a-z0-9._-]*", project):
        raise ValueError("one managed registry authority/platform project required")
    anonymous = {p["reference"] for p in config["anonymous_images"] if set(p) == {"reference", "evidence_sha256"} and re.fullmatch(r"[0-9a-f]{64}", p["evidence_sha256"])}
    if len(anonymous) != len(config["anonymous_images"]) or set(config["support_images"]) != {"driver", "launcher", "material_copy"}:
        raise ValueError("exact support images and finite anonymous pull evidence required")
    for reference in config["support_images"].values():
        image_authority(reference, authority, project, anonymous)
    if len(config["profiles"]) != 4 or {p["id"] for p in config["profiles"]} != PROFILES:
        raise ValueError("exact four fixed profile inputs required")
    if destination.exists():
        raise ValueError("compile destination must be new")
    destination.mkdir(parents=True)
    profiles = []
    for profile in sorted(config["profiles"], key=lambda p: p["id"]):
        if set(profile) != {"id", "compile_config"}:
            raise ValueError("profile takes only its explicit compile config")
        identifier = profile["id"]
        implementation, device = PROFILE_CONTRACT[identifier]
        compile_config = read_json(profile["compile_config"])
        if compile_config.get("admission_profile") != identifier or compile_config.get("owner_config_map") != "modeldev-step-owner" or compile_config.get("kind", "GENERAL_TRAINING") != ("GENERAL_TRAINING" if implementation == "builtin.mlp" else "FINETUNING"):
            raise ValueError("fixed profile, kind and step ConfigMap required")
        image_authority(compile_config["component_image"], authority, project, anonymous)
        output = destination / "ir" / (identifier + ".yaml")
        output.parent.mkdir(exist_ok=True)
        subprocess.run([sys.executable, str(compiler), "--config", str(Path(profile["compile_config"]).resolve()), "--output", str(output)], check=True)
        raw = output.read_bytes()
        ir_struct(raw)
        output.chmod(0o444)
        immutable(destination / "compile" / (identifier + ".json"), canonical(compile_config))
        profiles.append({"id": identifier, "implementation_id": implementation, "device_mode": device, "namespace": "ani-system", "ir_file": str(output.resolve()), "ir_sha256": sha(raw), "compile_config_sha256": sha(canonical(compile_config)), "component_image": compile_config["component_image"]})
    manifest = {"schema": "ani.modeldev.prepared-materials.v1", "source_commit": config["source_commit"], "source_tree": config["source_tree"], "kfp_source_sha": PINNED_KFP_SHA, "registry_authority": authority, "platform_project": project, "anonymous_images": config["anonymous_images"], "support_images": config["support_images"], "profiles": profiles}
    immutable(destination / "prepared.json", canonical(manifest))
    return manifest


def seal(published, materials, destination):
    if set(materials) != PROFILES or len(published["profiles"]) != 4 or {p["id"] for p in published["profiles"]} != PROFILES:
        raise ValueError("all four confirmed PipelineVersions and canonical materials required")
    if destination.exists():
        raise ValueError("sealed bundle directory must be new")
    destination.mkdir(parents=True)
    files, profiles = {}, []
    for profile in sorted(published["profiles"], key=lambda p: p["id"]):
        identifier = profile["id"]
        if profile.get("phase") != "CONFIRMED" or profile.get("namespace") != "ani-system" or not uuid_id(profile["pipeline_id"]) or not uuid_id(profile["version_id"]):
            raise ValueError("confirmed ani-system identity required")
        raw_ir = Path(profile["ir_file"]).read_bytes()
        if sha(raw_ir) != profile["ir_sha256"]:
            raise ValueError("IR source byte digest mismatch")
        ir_struct(raw_ir)
        raw_material = Path(materials[identifier]).read_bytes()
        material = json.loads(raw_material)
        if canonical(material) != raw_material:
            raise ValueError("material must be existing Go canonical output")
        implementation = material.get("implementation", {})
        if implementation.get("implementation_id") != PROFILE_CONTRACT[identifier][0] or implementation.get("pipeline_id") != profile["pipeline_id"] or implementation.get("pipeline_version_id") != profile["version_id"] or implementation.get("pipeline_ir_sha256") != profile["ir_sha256"]:
            raise ValueError("material PipelineVersion association mismatch")
        material_name = ".".join(PROFILE_CONTRACT[identifier]) + ".json"
        for name, raw in (("ir/" + identifier + ".yaml", raw_ir), (material_name, raw_material)):
            immutable(destination / name, raw)
            files[name] = sha(raw)
        profiles.append({**{k: v for k, v in profile.items() if k != "ir_file"}, "ir_file": "/etc/modeldev-materials/ir/" + identifier + ".yaml", "material_file": "/etc/modeldev-materials/" + material_name, "material_sha256": sha(raw_material)})
    manifest = {**{k: v for k, v in published.items() if k not in {"profiles", "schema"}}, "schema": "ani.modeldev.material-bundle.v1", "profiles": profiles, "files": files}
    immutable(destination / "manifest.json", canonical(manifest))
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    compile_parser = sub.add_parser("compile")
    compile_parser.add_argument("--config", type=Path, required=True)
    compile_parser.add_argument("--output", type=Path, required=True)
    compile_parser.add_argument("--compiler", type=Path, default=Path(__file__).resolve().parents[2] / "pipelines/cpu_p01.py")
    seal_parser = sub.add_parser("seal")
    seal_parser.add_argument("--published", type=Path, required=True)
    seal_parser.add_argument("--materials", type=Path, required=True)
    seal_parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "compile":
        prepare(read_json(args.config), args.output, args.compiler)
    else:
        seal(read_json(args.published), read_json(args.materials), args.output)


if __name__ == "__main__":
    main()
