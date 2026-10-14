---
status: accepted
---

# ModelDev owns admission and durable exit retention as separate responsibilities

ModelDev will ship one repository-owned `cmd/ani-modeldev-webhook` for the KFP step Pods of both GENERAL_TRAINING and FINETUNING, using finite typed namespace bindings and immutable Release profiles. The webhook performs CREATE admission, reads the original namespace/Workflow identities, and applies approved safety fields and step retention; a separately frozen, noncomputational KFP backend driver branch receives only its role and retention marker, while training Pod retention remains in the frozen Trainer Runtime template. ModelDev's background owner will persist independently verified exits before a conditional removal of its own step/training finalizer, then automatically reclaim only terminal, exactly associated Workflow step Pods. Training log Pods, PVCs and publications remain retained; computation controller disposal continues through the existing explicit audited CleanupPlan. Other controllers' finalizers and unknown writers are preserved.

This separation keeps admission free of execution state writes and prevents Pod deletion from racing ahead of the evidence needed by later steps and closure. The current authorization covers implementation, Fedora validation and reviewable deployment/migration material; registration and replacement of shared hooks remain unperformed and require separate authorization. Code readiness without registration does not establish CORE_FLOW, M3-A or live cleanup acceptance.
