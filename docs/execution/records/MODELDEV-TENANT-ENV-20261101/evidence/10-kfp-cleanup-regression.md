# KFP/cleanup contract-fixture repair

Only test fixtures were changed in this follow-up; production mutation identity, snapshot owner, four-root and cleanup guards were preserved.

- KFP fixtureClient already installed WithMutationIdentity, but shared admission Bind derives a new Namespace UID/BindingID/Experiment when migrating from a different tenant. The explicit boundary fixture now authors its target tenant Namespace before Bind, retaining its independently handwritten original UID/Experiment tuple so the boundary identity source and expected official KFP responses refer to the same environment.
- Cleanup resolution now freezes the exact Snapshot.DispatchOwner, supplies all four KFP runtime parameters, and gives its external Namespace double the required Network owner/tenant labels. It continues to obtain actual owner proof through production verification; no precomputed proof substitutes for it.
- Existing negative cases continue to reject mismatched UID, inactive/foreign Namespace, wrong KFP binding, unsafe cleanup children, writers and retention evidence.

Fedora /home/chabking/tenantenv-20261101/modeldev-environment:

`GOTOOLCHAIN=auto go test ./internal/data/kfp ./internal/data/cleanup -count=1`

Exit0: KFP0.680s; cleanup1.694s. Output: 10-kfp-cleanup-regression.log. Formatting was run only on Fedora and copied back. git diff --check for the two packages returned0. No cluster writes or real acceptance are established by this test result.
