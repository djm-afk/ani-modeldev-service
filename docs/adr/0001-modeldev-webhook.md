---
status: accepted
---

# ModelDev owns admission and durable exit retention as separate responsibilities

ModelDev ships one repository-owned `cmd/ani-modeldev-webhook` for the KFP step Pods of both GENERAL_TRAINING and FINETUNING, using finite typed namespace bindings and immutable Release profiles. The webhook performs CREATE admission, reads the original namespace/Workflow identities, and applies approved safety fields and step retention; a separately frozen, noncomputational KFP backend driver branch receives only its role and retention marker, while training Pod retention remains in the frozen Trainer Runtime template.

ModelDev's background owner persists independently verified exits before conditionally removing only its own step/training finalizers. After CLOSED and step cleanup, it durably captures the original training logs and reclaims terminal computation resources under their original UID and creation fences, processing parents before dependents. Durable evidence remains available to later verification after the resources disappear. Retries are bounded; uncertain associations or writers retain a NEEDS_REVIEW receipt. PVCs and publications remain retained, and explicit audited workspace or artifact cleanup has separate authorization. Other controllers retain ownership of their finalizers, including Kubernetes garbage collection's `orphan` marker.

This separation keeps admission free of execution state writes and prevents Pod deletion from racing ahead of the evidence needed by later steps and closure. Registration, changes to shared admission policy and live deployment follow their separately authorized scope and actual evidence. Code readiness or registration alone does not establish CORE_FLOW, M3-A or live cleanup acceptance.
