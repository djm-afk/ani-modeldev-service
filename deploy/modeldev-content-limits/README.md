# One deployment content limit

This offline renderer consumes two complete **JSON** inputs: the current
ModelDev config and the actual Model provider `apps/v1` Deployment, plus its
explicit container name. It does not read arbitrary YAML. The cutover operator
can use the existing YAML reader to convert the current ConfigMap value to JSON
before invoking this tool. Preserve mounted references and other private config
in those protected input files; do not print them.

`MODELDEV_MAX_CONTENT_BYTES` is required and must be a positive decimal int64.
There is no default, unlimited value or fallback to an input's previous limit.
It is the only deployment parameter used for both
`runtime.limits.max_content_bytes` and that Model container's environment entry.
All other configuration and provider environment entries are preserved.

Run on the task's Fedora source tree with protected, complete input files:

```sh
MODELDEV_MAX_CONTENT_BYTES=4294967296 python3 deploy/modeldev-content-limits/render.py \
  --modeldev-config /private/cutover/modeldev-config.json \
  --model-provider-deployment /private/cutover/model-provider-deployment.json \
  --model-provider-container model \
  --output /private/cutover/content-limits-4g
```

The container name is an explicit input; use the actual provider container name.
Changing the environment value and choosing a new output directory renders a new
package without changing source or recompiling either service. The example
4 GiB value does not establish available training resources or a product cap.

After validating all inputs, the renderer writes `managed-runtime.yaml` and
`model-provider.json` as mode `0600` files in a private staging directory, fsyncs
them, then atomically renames the complete directory into the new output path.
The published directory is mode `0700`; an existing output path is rejected.
`managed-runtime.yaml` contains JSON, which is valid YAML for the ModelDev loader.
No third policy manifest is generated. A failed validation creates no output.

The renderer never applies the Deployment, updates a ConfigMap or restarts a
service. Task 09 must mount the rendered runtime config, review/apply the rendered
provider Deployment and restart the responsible services, then check their
effective values agree. Existing Model product environment settings are retained.

Targeted verification on Fedora:

```sh
python3 -m unittest discover -s deploy/modeldev-content-limits -p test_render.py -v
```
