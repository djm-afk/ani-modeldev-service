# Shared ModelDev webhook

The v2 configuration is one platform configuration with four fixed profiles,
the shared immutable bundle file/digest, cluster identity, and exact controller
principals. There is no startup tenant or Namespace binding array. The example
uses deliberately non-deployable placeholder facts; actual values must come
from deployment evidence and the four published definitions.

For each protected Pod CREATE the handler reads the live standard Namespace,
immutable `modeldev-execution-environment` projection, and original SA. It
validates canonical projection bytes, bundle/material identities, original UIDs,
exact pull references, and the original owner chain. Driver and business step
Pods both validate the four root parameters from the Workflow's root-driver
runtime-config before entering their respective branches. A missing projection
denies the matched request. Ordinary Pods do not enter the remote webhook.

The renderer emits one global registration with OR matchConditions for reserved
SAs, metadata/finalizers, workspace PVCs, and every artifact Secret reference.
It emits no tenant-specific registration or Namespace selectors. The five
namespaced runtime roles and platform permissions are owned by the native
bootstrap renderer.

Material ConfigMap bytes are copied by the webhook binary's bounded init mode
into regular read-only files. `--copy-materials-from /source
--copy-materials-to /materials --copy-materials-manifest-sha256 SHA256` verifies
the original manifest and all eight exact files; the normal readers keep
O_NOFOLLOW. The renderer references `modeldev-materials-<bundle SHA prefix>`
created by the shared material owner. It does not create a second bundle.

The service's separately pinned public platform configuration uses a second
init invocation: `--copy-platform-from /source/platform.json
--copy-platform-to /platform/platform.json --copy-platform-sha256 SHA256`.
It verifies the original file bytes, installs one regular `0400` file, and
rejects changed bytes or a symlink destination. The platform file remains
outside the material manifest because it contains the material bundle digest.

Render, build, and component tests belong on Fedora. Actual server dry-run,
ordinary workload coexistence with webhook unavailable, and A/B training remain
separate deployment acceptance evidence.

The registration renderer requires `--platform-principals`, a JSON array of the
same existing trusted usernames used by the native workload guard. These actors
skip the ModelDev Pod webhook through an API-server matchCondition. This does
not change the Go service configuration, issue credentials or grant RBAC.
Other creators retain the existing business validation and required mutation.
