# Runtime verification

The generated service keeps executable runtime checks beside the source. From a
clean checkout, run:

```bash
make tools
python3 scripts/with-test-postgres.py make verify
```

`make verify` checks that typed configuration, public protobuf, sqlc queries,
and shared contract vectors regenerate without a content change, rejects stale
formatting or module metadata, and runs `go test`,
`go vet`, `go build`, and `go mod verify`.

The runtime integration tests start real loopback gRPC and admin HTTP listeners.
They check gRPC health, disabled reflection, health/readiness/metrics responses,
Kratos error encoding, middleware recovery/metadata/validation, request metrics,
trace-correlated structured logging, and graceful application stop. These tests
prove only the generic local runtime. They do not prove a database, broker,
provider, external telemetry backend, container, Kubernetes deployment, or
business contract.

The execution repository tests separately require real PostgreSQL. The wrapper
starts a digest-pinned, resource-limited container, keeps its credentials in a
private temporary location, and passes separate migration/runtime references to
the test process. The runtime role is NOSUPERUSER/NOBYPASSRLS; each test applies
the versioned migration in its own schema and grants only required operations.
Afterward the wrapper checks the ownership label and removes only its exact test
container. Fedora may instead use the already prepared task-exclusive database
by supplying both protected `CPU_P01_TEST_DATABASE_*_URL` references and invoking
`make verify`. Missing dependencies fail with behavior NOT_RUN, not a skipped
security check. These real database results do not prove the target cluster.

Run these commands on Fedora against a fixed source commit. The
workstation is used only to edit, review, and transfer source and results. The
default configuration remains an unready shell. Explicit managed runtime
configuration assembles the persistent execution/environment repositories and
real business adapters; all required dependencies and the pinned platform
specification must be present. Its process health or gRPC SERVING status cannot
declare absent dependencies ready. Production-composition tests exercise real
listeners while preserving this distinction. A ready listener also does not
prove any tenant environment is READY or that real training passed. Current
configuration and the separate component/live evidence layers are defined by
[dynamic tenant environments](modeldev-tenant-environments.md).

For vulnerability and secret scans, a reproducible CycloneDX JSON runtime SBOM,
and notice/license-inventory consistency checks (with `jq` available):

```bash
make supply-chain-tools
make audit
```

Vulnerability results are valid only for the scanner and database state at the
time of execution. The pinned Gitleaks module scans all local Git refs with its
embedded detector set and redacts any finding in command output; CI requests a
full-history checkout before running it. The SBOM
generator requires a committed revision, ignores only its own output while
checking cleanliness, and builds an isolated synthetic Git snapshot from all
tracked files except the prior SBOM. This prevents both a self-reference and
accidental discovery of a parent Git repository through `TMPDIR`.

The committed SBOM targets the Linux/amd64 runtime graph and excludes test-only
dependencies. Fedora and CI install the same official CycloneDX v1.12.0
Linux/amd64 release executable with `scripts/install-cyclonedx`; these SBOM gates
require a Linux/amd64 host, `curl`, `tar`, and `sha256sum`. The script pins both
the release archive and extracted executable SHA-256, and `check-cyclonedx`
checks the binary hash before executing it, then retains the version and Go
module identity checks. The dedicated versioned tool path leaves an existing
Go-installed `cyclonedx-gomod` untouched; a mismatching cached binary is rejected.

The pins were checked against the
[official v1.12.0 release](https://github.com/CycloneDX/cyclonedx-gomod/releases/tag/v1.12.0)
and its `cyclonedx-gomod_1.12.0_checksums.txt`. The archive SHA-256 is
`004b9f5cc595b797fb5423e2ae4c97bcf0f18c712ed2faee1640b09e5efd6d15`;
the executable SHA-256 is
`2238adac4b5e36926d388461e72ef1ae8caaf4ac11c221ac5fab4e76d7a29861`.
Tool updates must review and change both pins and the Makefile version together.
Byte reproducibility requires the same source snapshot and pinned executable;
the full SBOM comparison retains all generator and dependency hashes.

For a newly generated repository, first commit the generated source, then run
`make supply-chain-tools` and `make audit`, review the result, and commit
`docs/scaffold/bom.cdx.json`. The CI gate rejects a missing or stale SBOM and
reruns the scanner and notice/license-evidence checks. The retained upstream
notice is not a project license; choose the service's license before publication.
