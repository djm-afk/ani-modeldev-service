# Contract and file ownership freeze

Source main references match spec 1.1, fetched over SSH. Resource used ssh.github.com:443 after github.com:22 closed twice; no global SSH setting changed.

ModelDev root owns API source/generation, cmd/conf/server composition, dispatch/close and execution projections. Environment agent owns biz/tenant_environment.go, data/tenantenvironment, contract/tenantenvironment, service/tenant_environment and 0002 migration. Snapshot agent owns cpup01/protobuf, dynamic facts, STS/Model adapters, IR/KFP, runtime plans and step identity. Resource agent owns the isolated Resource worktree and issue 02. Governance has a separate worktree for 07 after shared contract completion.

Environment RPC wire shape follows contracts/environment-api.md exactly. ErrorReason 38 appended, existing reserved tags retained. ExecutionSnapshot new wire tags are 22 DispatchOwner and 23 ManagedImageAccess; RuntimeIdentityRefs original SA UIDs are 6/7/8. Current schema only v4. Names, projection canonical boundary, four root parameters, state/phase/reason, budgets and grant scope follow the supplied spec.

`api-generation.log`: Fedora pinned `make api`, exit 0. `config-generation.log`: Fedora pinned `make config`, exit 0. These are source-generation evidence, not service or live acceptance. Full `make verify` remains required before committing integrated code.

Specific baseline conflict: scripts/verify-source:31 rejected every migration other than 0001_current_baseline.up.sql; spec 3.2 and 8.2 require a new environment table and execution projections, with old v3 drained before cutover. Minimal correction: explicitly admit only new 0002/0003 migrations in that gate, add those same inputs to sqlc and the isolated PostgreSQL fixture. Existing baseline is unchanged and no compatibility reader is added.

All new live acceptance remains NOT_RUN. Unknown deployment inputs remain null in deployment-inputs.json and are owned by 08/09/10, not guessed during 01.
