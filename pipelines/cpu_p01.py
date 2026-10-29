"""The single CPU-P01 KFP assembly entry; compile on Fedora only."""
import argparse
import hashlib
import json
import re

from kfp import compiler, dsl, kubernetes
from kubernetes.utils.quantity import parse_quantity


def compile_pipeline(configuration, output):
    required = {"component_image", "owner_config_map",
                "storage_class", "workspace_size", "workspace_access_mode", "admission_profile",
                "execution_timeout_max_seconds", "component_resources"}
    if set(configuration) not in (required, required | {"kind"}):
        raise ValueError("all frozen deployment bindings are required")
    kind = configuration.get("kind", "GENERAL_TRAINING")
    if kind not in ("GENERAL_TRAINING", "FINETUNING"):
        raise ValueError("unsupported managed execution kind")
    maximum_timeout = configuration["execution_timeout_max_seconds"]
    if type(maximum_timeout) is not int or maximum_timeout <= 0 or maximum_timeout > 9223372036:
        raise ValueError("explicit finite execution maximum is required")
    finetuning = kind == "FINETUNING"
    roles = {"workspace-name", "prepare", "train-wait", "collect", "publish", "close", "finalize-close"}
    if finetuning:
        roles |= {"export", "verify", "register"}
    fields = {"cpu_request", "cpu_limit", "memory_request", "memory_limit",
              "ephemeral_storage_request", "ephemeral_storage_limit"}
    resources = configuration["component_resources"]
    if not isinstance(resources, dict) or set(resources) != roles:
        raise ValueError("the exact managed role resource set is required")
    for role, values in resources.items():
        if not isinstance(values, dict) or set(values) != fields:
            raise ValueError("all six explicit component resources are required")
        quantities = {}
        for name, value in values.items():
            if not isinstance(value, str) or not value or value.strip() != value:
                raise ValueError("component resources must be positive quantities")
            try:
                quantity = parse_quantity(value)
            except (ValueError, TypeError, ArithmeticError) as error:
                raise ValueError("component resources must be positive quantities") from error
            if not quantity.is_finite() or quantity <= 0:
                raise ValueError("component resources must be positive quantities")
            quantities[name] = quantity
        for resource in ("cpu", "memory", "ephemeral_storage"):
            if quantities[resource + "_request"] > quantities[resource + "_limit"]:
                raise ValueError("component requests must not exceed limits")
    profile = configuration["admission_profile"]
    if not isinstance(profile, str) or not re.fullmatch(r"[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?", profile):
        raise ValueError("a frozen ModelDev admission profile DNS label is required")
    if not re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", configuration["component_image"]):
        raise ValueError("component image must use a manifest digest")
    for key in ("owner_config_map", "storage_class"):
        if not re.fullmatch(r"[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?", configuration[key]):
            raise ValueError("invalid frozen Kubernetes binding")
    if not re.fullmatch(r"[1-9][0-9]*(?:Mi|Gi)", configuration["workspace_size"]):
        raise ValueError("explicit positive workspace capacity is required")
    if configuration["workspace_access_mode"] not in ("ReadWriteOnce", "ReadWriteMany"):
        raise ValueError("workspace must support sequential component and Trainer mounts")

    def container(step, execution_id, spec_hash, run_id, extra=()):
        return dsl.ContainerSpec(
            image=configuration["component_image"],
            command=["/ani-modeldev-step", step],
            args=["--config", "/etc/modeldev-step/config.json",
                  "--execution-id", execution_id, "--spec-hash", spec_hash,
                  "--run-id", run_id, *extra],
        )

    @dsl.container_component
    def workspace_name(execution_id: str, name: dsl.OutputPath(str)):
        return dsl.ContainerSpec(image=configuration["component_image"],
                                 command=["/ani-modeldev-step", "workspace-name"],
                                 args=["--execution-id", execution_id, "--output", name])

    @dsl.container_component
    def prepare(execution_id: str, spec_hash: str, run_id: str, pvc_name: str):
        return container("prepare", execution_id, spec_hash, run_id, ["--pvc-name", pvc_name])

    @dsl.container_component
    def train_wait(execution_id: str, spec_hash: str, run_id: str):
        return container("train-wait", execution_id, spec_hash, run_id)

    @dsl.container_component
    def export(execution_id: str, spec_hash: str, run_id: str):
        return container("export", execution_id, spec_hash, run_id)

    @dsl.container_component
    def verify(execution_id: str, spec_hash: str, run_id: str):
        return container("verify", execution_id, spec_hash, run_id)

    @dsl.container_component
    def collect(execution_id: str, spec_hash: str, run_id: str):
        return container("collect", execution_id, spec_hash, run_id)

    @dsl.container_component
    def publish(execution_id: str, spec_hash: str, run_id: str, candidate: dsl.OutputPath(str)):
        return container("publish", execution_id, spec_hash, run_id, ["--publication-ref-file", candidate])

    @dsl.container_component
    def close(execution_id: str, spec_hash: str, run_id: str, candidate: str = ""):
        return container("close", execution_id, spec_hash, run_id, ["--publication-ref", candidate])

    @dsl.container_component
    def register(execution_id: str, spec_hash: str, run_id: str, candidate: str):
        return container("register", execution_id, spec_hash, run_id, ["--publication-ref", candidate])

    @dsl.container_component
    def finalize_close(execution_id: str, spec_hash: str, run_id: str):
        return container("close", execution_id, spec_hash, run_id, ["--close-only"])

    def configure(task):
        task.set_caching_options(False).set_retry(0)
        values = resources[task.name]
        kubernetes.add_pod_label(task, "ani.modeldev/admission-profile", profile)
        kubernetes.add_pod_label(task, "ani.modeldev/step-role", task.name)
        digest = hashlib.sha256(json.dumps(values, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        kubernetes.add_pod_annotation(task, "ani.modeldev/component-resources-sha256", digest)
        task.set_cpu_request(values["cpu_request"]).set_cpu_limit(values["cpu_limit"])
        task.set_memory_request(values["memory_request"]).set_memory_limit(values["memory_limit"])
        if task.name in ("prepare", "export", "verify", "collect", "publish"):
            work = "/workspace/work/" + task.name
            for name, suffix in (("TMPDIR", "/tmp"), ("HF_HOME", "/hf"),
                                 ("HF_DATASETS_CACHE", "/hf/datasets"), ("MPLCONFIGDIR", "/matplotlib")):
                task.set_env_variable(name, work + suffix)
        kubernetes.set_timeout(task, maximum_timeout)
        kubernetes.set_security_context(task, run_as_user=10001, run_as_group=10001, run_as_non_root=True)
        kubernetes.use_config_map_as_volume(task, configuration["owner_config_map"], "/etc/modeldev-step")
        for name, path in (("ANI_POD_NAME", "metadata.name"), ("ANI_POD_UID", "metadata.uid"),
                           ("ANI_POD_NAMESPACE", "metadata.namespace")):
            kubernetes.use_field_path_as_env(task, name, path)
        kubernetes.empty_dir_mount(task, "component-tmp", "/tmp", size_limit=values["ephemeral_storage_limit"])
        return task

    # KFP/Argo includes this name in component Pod names; KCN also uses each
    # Pod name as an annotation suffix, limited to 63 bytes by Kubernetes.
    @dsl.pipeline(name="ft" if finetuning else "md")
    def pipeline(execution_id: str, spec_hash: str):
        # KFP records display_name as the managed role checked by ModelDev.
        run_id = dsl.PIPELINE_JOB_ID_PLACEHOLDER
        finalizer = configure(finalize_close(execution_id=execution_id, spec_hash=spec_hash, run_id=run_id)).set_display_name("close-finalizer")
        with dsl.ExitHandler(exit_task=finalizer, name="managed-execution"):
            # This official KFP resource primitive is interpreted by its backend;
            # argostub/createpvc is not an image that this application runs.
            named = configure(workspace_name(execution_id=execution_id))
            workspace = kubernetes.CreatePVC(
                pvc_name=named.output, access_modes=[configuration["workspace_access_mode"]],
                size=configuration["workspace_size"], storage_class_name=configuration["storage_class"],
            ).set_caching_options(False).set_retry(0)
            prepared = configure(prepare(execution_id=execution_id, spec_hash=spec_hash, run_id=run_id, pvc_name=workspace.output))
            trained = configure(train_wait(execution_id=execution_id, spec_hash=spec_hash, run_id=run_id).after(prepared))
            predecessor = trained
            if finetuning:
                exported = configure(export(execution_id=execution_id, spec_hash=spec_hash, run_id=run_id).after(trained))
                verified = configure(verify(execution_id=execution_id, spec_hash=spec_hash, run_id=run_id).after(exported))
                for task, paths in ((exported, ("inputs", "training", "reports/train", "export", "reports/export", "work/export")),
                                    (verified, ("inputs", "training", "reports/train", "export", "reports/export", "reports/verify", "work/verify"))):
                    for path in paths:
                        kubernetes.mount_pvc(task, workspace.output, "/workspace/" + path, sub_path=path)
                predecessor = verified
            collected = configure(collect(execution_id=execution_id, spec_hash=spec_hash, run_id=run_id).after(predecessor))
            published = configure(publish(execution_id=execution_id, spec_hash=spec_hash, run_id=run_id).after(collected))
            for task in (prepared, collected, published):
                kubernetes.mount_pvc(task, workspace.output, "/workspace")
            # KFP writes its CA bundle before invoking prepare. A native PVC
            # subPath mount lets kubelet create TMPDIR before that launcher.
            kubernetes.mount_pvc(prepared, workspace.output, "/workspace/work/prepare/tmp", sub_path="work/prepare/tmp")
            # Admission projects the root mount to read-only, keeping only
            # publication and the role's work directory writable.
            for task in (collected, published):
                kubernetes.mount_pvc(task, workspace.output, "/workspace/publication", sub_path="publication")
                kubernetes.mount_pvc(task, workspace.output, "/workspace/work/" + task.name, sub_path="work/" + task.name)
            completed = published
            if finetuning:
                completed = configure(register(execution_id=execution_id, spec_hash=spec_hash, run_id=run_id, candidate=published.output).after(published))
                kubernetes.mount_pvc(completed, workspace.output, "/workspace")
            closed = configure(close(execution_id=execution_id, spec_hash=spec_hash, run_id=run_id, candidate=published.output).after(completed))
            kubernetes.mount_pvc(closed, workspace.output, "/workspace")
        # No DeletePVC: publication failure must retain the unique outputs.

    compiler.Compiler().compile(pipeline_func=pipeline, package_path=output)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    parser.add_argument("--output", required=True)
    options = parser.parse_args()
    with open(options.config, encoding="utf-8") as source:
        compile_pipeline(json.load(source), options.output)
