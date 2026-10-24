---
status: accepted
---

# ModelDev owns admission and durable exit retention as separate responsibilities

ModelDev ships one repository-owned `cmd/ani-modeldev-webhook` for the KFP step Pods of both GENERAL_TRAINING and FINETUNING, using finite typed namespace bindings, implementation materials and each execution's frozen RuntimePlan. The webhook performs CREATE admission, reads the original namespace/Workflow identities, and applies the fixed safety fields and step retention; a separately frozen, noncomputational KFP backend driver branch receives only its role and retention marker, while training Pod retention remains in the execution's compiled Trainer Runtime plan.

The v2.1 contract supersedes this decision's former Release-profile terminology. Implementation and ComputePreset are separate selections; read-only PrepareAdmission and FinalizeAdmission produce the single typed execution snapshot without a Release approval or implementation/resource combination allowlist. CREATE admission verifies the original frozen graph and resources; it does not resolve current defaults or grant a business execution permit. Historical Release-based receipts remain evidence of their original revision and are not rewritten as v2.1 receipts.

ModelDev's background owner persists independently verified exits before conditionally removing only its own step/training finalizers. After CLOSED and step cleanup, it durably captures the original training logs and reclaims terminal computation resources under their original UID and creation fences, processing parents before dependents. Durable evidence remains available to later verification after the resources disappear. Retries are bounded; uncertain associations or writers retain a NEEDS_REVIEW receipt. PVCs and publications remain retained, and explicit audited workspace or artifact cleanup has separate authorization. Other controllers retain ownership of their finalizers, including Kubernetes garbage collection's `orphan` marker.

This separation keeps admission free of execution state writes and prevents Pod deletion from racing ahead of the evidence needed by later steps and closure. Installing the webhook, changes to shared admission policy and live deployment follow their separately authorized scope and actual evidence. Code readiness or webhook registration alone does not establish CORE_FLOW, M3-A or live cleanup acceptance.
