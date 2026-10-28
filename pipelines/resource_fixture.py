"""Explicit operator resource choices for compiler behavior fixtures only."""


def component_resources(kind="GENERAL_TRAINING"):
    roles = ["workspace-name", "prepare", "train-wait", "collect", "publish", "close", "finalize-close"]
    if kind == "FINETUNING":
        roles += ["export", "verify", "register"]
    return {role: {"cpu_request": "1250m", "cpu_limit": "2500m", "memory_request": "3Gi",
                   "memory_limit": "9Gi", "ephemeral_storage_request": "17Gi",
                   "ephemeral_storage_limit": "21Gi"} for role in roles}
