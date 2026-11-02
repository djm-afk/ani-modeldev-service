# Current-source configuration/documentation cleanup

- Changed current entry points: CONTEXT.md; configs/examples/managed-runtime.yaml; docs/adr/0001-modeldev-webhook.md; docs/modeldev-webhook.md; docs/runtime-verification.md; docs/design/cpu-p01-{managed-admission-facts,admission-resolution}.md.
- Added effective contract: docs/modeldev-tenant-environments.md (platform/projection/webhook/v4 schemas, single platform inputs, regular-file init copy, scopes/STS and artifact separation, original UID authority, bounded Ensure/Get, native policies and distinct evidence layers).
- Marked historical source explicitly: docs/design/cpu-p01-{snapshot,domain-model}.md, docs/finetuning-{p0-plan,next-handoff}.md. Updated static enrollment/deployment paragraphs in docs/finetuning-core-flow.md. Historical actual IDs/hashes/results were not relabeled as current acceptance.
- Removed old active example fields binding_file/binding_sha256/dispatch_batch_size; replaced old STS tenant-map instruction. Added required catalogue and Resource mTLS input blocks. Command listener timeout is 30 seconds so the 25-second Ensure budget is not truncated by the example's old ten-second budget.
- ADR 0002 original owner/quota-release authority was not modified. No Reserve, charge-release, Inference or cluster action was performed by this documentation cleanup.

Validation: Fedora command GOTOOLCHAIN=auto go run .scratch/modeldev-dynamic-tenant-environment/example_config_probe.go, exit 0. The probe expands example placeholders to explicit synthetic absolute file references/endpoints, parses strict YAML, strictly decodes the current generated protobuf, and runs Bootstrap.Validate. It then adds retired runtime.binding_file and verifies strict decode rejects it. Output: 10-example-config-check.log. This proves current example shape/validation, not material loading, actual IAM, adapter availability or startup/deployment.

Source whitespace check: git diff --check over the changed documentation/example and process recovery test, exit 0.

NOT_RUN: actual platform/credential loading, native API-server probes, deployment, cleanup/cutover and real A/B acceptance; these remain owned by tasks 09/10 and require designated-cluster evidence. Old deeper design records remain labeled historical rather than becoming a compatibility entry point.
