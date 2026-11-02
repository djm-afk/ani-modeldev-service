# B GPU_SHARED preparation

Status: source path supported; live preflight BLOCKED; no mutation or network probe.

The current ledger has no tenant quota override. Effective policy is tenant.plan_id → PlanQuota. Do not add shared-memory quota to shared plan2. Existing normal APIs can create a task-private copy of B's full original plan2 metadata/modules/quotas, add only gpu.shared_memory_mib=49140, verify the complete policy, then PUT /api/v1/admin/tenants/40 with {"id":40,"data":{"planId":<private_id>},"updateMask":"planId","allowMissing":false}. This changes B's pointer only; no new compute SKU or role/grant is required by this preparation.

The49140 candidate is grounded in an actual normal A policy create/readback receipt, plan3/tenant2, captured2026-10-23T08:43:16Z; receipt SHA2567690c764125b71929f779f894c358b531f88c4aa31ba2a20ae88e3764c5116b3. This is historical amount provenance, not current A/B policy state. Existing accepted Shared2 preset4d34373d-be80-4c17-8ee3-fe24ec0e12d8 requests24570MiB/one device, profileac998f7a-c39a-49aa-8aae-d932d567116a version1, SHARED_FIXED. Exact B visibility, active profile digest, available capacity and fine-tuning readiness remain unverified.

Precise Plan/Module/Quota create DTOs, readback routes and source hashes are in the JSON. The live B preflight selected only relevant modules/GPU policies, so it is insufficient for a full private-plan copy. The plan ID must come from exact-name GET after normal API creation. No native idempotency or expected-version token exists. Plan names and quota(plan,code) are unique; module(plan,module) is not uniquely guarded. Use one writer and exact issued-request/readback records; never blindly replay an uncertain POST. Tenant Update is not a server compare-and-swap: concurrent subscription/policy writes must be quiesced and the original B UUID/current plan rechecked immediately before assignment.

Formal environment API catalog sync and the existing manage/view permission migration remain required. GPU listing also checks the current GET /api/v1/accelerator/profiles permission in addition to modeldev:compute-preset:read; that current B grant was not part of the previous read-only query. Do not bypass it for a test.

An existing local Model deployment receipt gives the exact historical database reference: primaryMD/.scratch/modeldev-execution-v2.1/records/model-runtime-deploy-preflight.json lines42/195/205: ani_model, ani-system/ani-model-database:dsn, Pod-local127.0.0.1:5432/ani_model. This turn read no DSN values and made no Model query.
