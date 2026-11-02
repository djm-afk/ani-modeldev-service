#!/usr/bin/env python3
"""Bind explicit verified platform inputs to the one sealed four-profile bundle.

This does not fill missing live inputs. The template contains platform-only
fields from tenantenvironment.Config; no tenant binding or secret value belongs
in it. The final Go config loader remains the authoritative strict validator.
"""
import argparse
from pathlib import Path
from publisher import PROFILES, canonical, read_json
from render import validate_bundle


def material_configuration(template, directory):
    manifest, digest, _ = validate_bundle(directory)
    def check(value, path="platform"):
        if value is None:
            raise ValueError("unverified required deployment input: " + path)
        if isinstance(value, dict):
            if set(value) & {"tenant_id", "namespace_name", "namespace_uid", "bindings", "facts_files", "access_key", "secret_key", "password", "token", "dsn", "kubeconfig"}:
                raise ValueError("tenant bindings and secret values are forbidden in platform inputs")
            for key, child in value.items():
                check(child, path + "." + key)
        elif isinstance(value, list):
            for index, child in enumerate(value):
                check(child, path + "." + str(index))
    check(template)
    forbidden = {"profiles", "ir_sources", "material_bundle_digest", "schema_version", "registry_authority", "platform_project"}
    if set(template) & forbidden:
        raise ValueError("bundle-derived fields must not be overridden")
    profiles = [{key: profile[key] for key in ("id", "pipeline_id", "version_id", "ir_sha256", "material_sha256")} for profile in manifest["profiles"]]
    if {profile["id"] for profile in profiles} != PROFILES:
        raise ValueError("exact fixed four-profile bundle required")
    return {**template, "schema_version": "ani.modeldev.tenant-environment-platform.v1", "registry_authority": manifest["registry_authority"], "platform_project": manifest["platform_project"], "material_bundle_digest": digest, "profiles": profiles, "ir_sources": [{"profile_id": profile["id"], "file": profile["ir_file"], "sha256": profile["ir_sha256"]} for profile in manifest["profiles"]]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--template", type=Path, required=True)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    value = material_configuration(read_json(args.template), args.bundle)
    with args.output.open("xb") as output:
        output.write(canonical(value))


if __name__ == "__main__":
    main()
