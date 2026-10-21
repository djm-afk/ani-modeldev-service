# CPU 受管微调 M3-A 集群侧复核报告

复核日期：2026-10-16
复核人：接手验证方（独立于原作者）
复核对象：2026-10-15 已完成的 CORE_FLOW / M3-A
对应文档：[CPU 微调手动验收手册](finetuning-manual-test.md)、[执行主链](finetuning-core-flow.md)、[后续工作交接](finetuning-next-handoff.md)

---

## 0. 复核性质与边界（先读）

- 本报告是**独立只读复核**：全程仅使用 `kubectl get / logs / exec psql SELECT`，**未创建、修改、删除任何集群对象**。
- 复核范围 = **M3-A 的集群侧可见事实**（K8s 对象、Evidence 卷 native JSON、真实 PostgreSQL）。不重跑业务、不新建用例。
- **执行位置**：本地 Windows 经 `local-remote/ttyd-exec.py`（WebSocket + ttyd 协议）到远端 ttyd 终端，在 `ani-01` 上执行 kubectl/psql。
- 网络注意：本机默认出口与以太网出口到目标均超时，**只有 WLAN 源地址 `192.168.203.132` 可达**；每条远端命令均带 `--source-ip 192.168.203.132`。
- **不在本次复核范围（Fedora 侧材料，未取得）**：`.scratch/finetuning-cpu/records/*` 原始记录、helper 工具、workflow-watch 原始事件流、`.private/driver-state.json` 台账、`storage-receipt.json`、M09 的 `make verify` 门禁退出码。这些一律标 `NOT_VERIFIED`，**不从集群侧推断其存在**。
- 本文档**不写入任何凭据、签名 URL、私有 fixture 全文**。

---

## 1. 复核结论摘要

| 判定 | 数量 | 说明 |
| --- | --- | --- |
| 集群侧事实与手册一致 | M01–M08 主链 + 环境读回 | 未发现伪造、矛盾或与手册冲突之处 |
| 需组长澄清的差异 | 3 处 | 见 §4（driver 镜像 digest、source-commit 口径、kubeconfig 入口） |
| 无法在本机复核（Fedora） | 见 §5 | 标 NOT_VERIFIED，未据此下任何"通过"结论 |

**总体**：手册声称的 M3-A 结论，在**集群侧可见证据上高度可复核且自洽**。A/B 的 18 文件发布与 bundle SHA、A/B merged 权重确有差异、deadline/stop 时间戳、脱卷取回（reader）均逐项对得上。**3 处差异不影响 M3-A 定性，但记录口径需与组长对齐**（详见 §4）。

---

## 2. 环境与身份读回

| 项 | 手册/交接声称 | 实测 | 判定 |
| --- | --- | --- | --- |
| 集群 UID（kube-system） | `5277649d-e28d-4a0a-ae76-8354365c63bc` | 一致 | ✅ |
| TENANT 命名空间 UID | `8f79e7cc-769b-4b2d-87d3-a69c53c946f3` | 一致 | ✅ |
| SYSTEM 命名空间 UID | `f81b058f-91fb-4599-b96e-b700c605cc92` | 一致 | ✅ |
| 节点 | `172.16.101.10/.11/.12` | 三节点 InternalIP 一致 | ✅ |
| K8s 版本 | `v1.35.8` | 一致 | ✅ |
| 容器运行时 | — | containerd 2.3.4 | 记录 |
| 配额 | 19 PVC / 380Gi 已满 | `spec.hard` = `used` = 19 PVC / 380Gi | ✅ |
| API 入口 | 手册预期 `https://172.16.101.10:6443` | 本机 context 为 `https://lb.kubesphere.local:6443` | ⚠️ 差异，见 §4.3 |

**PVC 占用（19/19）**：A 轮 `ani-kfp-workspace-7ebc5706…`、B 轮 `ani-kfp-workspace-53c01c4e…`、以及 a16 / b7 / R2(`bc66c25a`) 等历史执行卷**全部 Bound 保留**。与手册"不删旧 PVC、需环境 owner 授权扩容"的约束一致。

**在跑组件**：
- `ani-modeldev-webhook`：**2/2 Ready**，generation 8（与手册"M08 正式 webhook 2 副本"一致）
- `core-flow-postgresql`：1/1 Running
- `finetuning-core-flow-20261014-admission`：1/1 Running
- **无活跃的业务驱动 Pod**（`app=core-flow-driver` 全部为 Succeeded/Completed/Error/Failed），满足手册"启动新用例前无 Pending/Running 旧驱动"的前提。

---

## 3. 逐用例复核

### M01（A：默认 0.0001/20）

- 驱动 Job `core-flow-a-f7b852e`：**Complete**，UID `a6808399-5bc7-4969-aa77-db1ca8d0d4d7`，`01:13:47Z → 01:31:11Z`（约 17 分钟）
- Evidence 卷 native（`core-flow-evidence-a-f7b852e` 日志）：execution `7ebc5706-2bb9-4254-958c-d6e40d3a669b`、operation `6b5d886e-…`
- **publication `10220103-efd0-571f-a5c6-a5e4d57f0886`**（与手册 §4 逐字一致）
- publication **18 个文件**，roles = `HF_MODEL / LORA_ADAPTER / METRICS / SUMMARY / EXPORT_REPORT / VERIFY_REPORT / SOURCE_METADATA`
- **bundle**：`executions/7ebc5706…/bundle.tar`，`size_bytes = 1992053248`，`sha256 = 9a90d00223fcd6e096f8463172265c88d5979a3751e707815cbc7c5b4666107d`
  → **与手册 §5 的字节数/SHA 逐字一致**
- `CloseReason = STEP_FAILED`、`ClosedAt = 2026-10-15T01:30:22.922301Z`、`CloseGeneration = 1`、`CloseReviewReason = ""`
- `model_registration = "provider unavailable; no READY"`（与手册"返回 MODEL_DEPENDENCY_UNAVAILABLE"一致）
- `ComputeCleanup.phase = APPLIED`
- 原计算 `TrainJob md-7ebc5706…` 独立 GET → **NotFound**（已回收）
- `closed_replay` 保持原 execution/operation，`Intent.finetuning = {}`（默认意图）

**判定：✅ 与手册一致。**

### M02（B：显式 0.0002/10）

- 驱动 Job `core-flow-b-f7b852e`：**Complete**，UID `8b1cc5b2-…`，`01:32:20Z → 01:49:05Z`（沿用 A 之后串行）
- DB 受理意图（`intent_canonical`）确认：`"finetuning":{"learning_rate":"0.0002","max_steps":10}`（**显式非默认**，与 A 的 `{}` 不同意图）
- publication `464d2798-484b-591a-9f27-29ec90da69e4`（与手册 §4 一致）、**18 个文件**
- **merged 权重差异复核**：
  - A `export/merged/model.safetensors`：sha256 `8534b212502d211c9ffe1c00a4eab9174e1f5deff0c1330e80afc04a112cfb8e`
  - B `export/merged/model.safetensors`：sha256 `1ecc8c85149c8dc041f91f90960abb8c240bba8632357ddacac6699f36ad6e1b`
  - **两者确实不同** ✅（满足手册"M02 B 实际 merged 权重 SHA 与 A 不同"）
- `Observation.Outcome = SUCCEEDED`，训练 Pod `md-53c01c4e…-node-0-0-dcf7k` `ExitCode = 0`
- `CloseReason = STEP_FAILED`、`ClosedAt = 2026-10-15T01:48:16.979656Z`、`publication_id = 464d2798…`

**判定：✅ 与手册一致。**

> 注：B 无独立 evidence producer Job（与 A 不同）。B 的 native 事实通过真实 PG 的 `modeldev_execution_runtimes.facts` 取回（`.scratch` 未取得，见 §5）。

### M03（脱离训练卷取回 / 离线加载）

`core-flow-readback-7ebc5706-v3`（**Complete**，113s）；`core-flow-readback-7ebc5706`（Failed，为早期版本，已被 v3 取代）。

reader 输出（`status: PASSED`）实质核对：

- `scope = CORE_FLOW_ARTIFACT_CONSUMPTION`，`execution_id = 7ebc5706…`，`source_commit = f7b852e…`
- `training_pvc_mounted = false` ✅（不挂训练 PVC）
- `credentials_role = "independent finite readback IAM"` ✅
- 对象核对：**18 文件 + output_manifest + main_tar = 20 对象**，每个均 `head_status=200`、`measured_bytes` 与 `object.size_bytes` 一致、`measured_sha256` 与登记 SHA 一致 ✅
- `main_tar`：`1992053248` 字节 / `9a90d002…`（与 A 发布 bundle 一致）
- `report_links.optimizer_steps = 20`、`source_samples = 64` ✅
- 离线消费：`consumer.dtype=float32`、`device=cpu`、`finite_logits=true`、`generated_text="Four"`、`torch=2.6.0+cpu`、`transformers=4.49.0`
- `not_proven = ["MODEL_READY", "FT29_PRODUCTION_INFERENCE"]` —— **明确声明不证明 Model READY / FT29**，与交接文档口径一致 ✅

**判定：✅ 与手册一致（且明确自我限定范围）。**

### M04（训练中 USER_STOP）

- 驱动 Job `core-flow-stop-9c8ab03`：**Complete**（4m40s）；其 `source_commit = 9c8ab03…`
- native execution `a52306f4-844f-48ec-8dec-25f30e2a0f91`
- DB：`close_reason = USER_STOP`、`close_generation = 1`、`close_requested_at = 2026-10-15 03:08:19.463679+00`、`closed_at` 有值
- `TrainingHandle.TrainJobUID = 2ab4c193-3c75-4935-acfe-a8d3856e366e`；`ComputeCleanup.phase = APPLIED`，requested 目标含该 TrainJob
- `Publication = None`（停止后未发布完整模型）✅
- close intent：`reason=USER_STOP`、`source_kind=GOVERNANCE`、`source_generation=1`、`owner_generation=1`

> 与手册"M04 训练 writer Running 后 USER_STOP … 同键重放"方向一致。（原 node 退出码 143 的逐 Pod 断言属 native 明细，本机仅能确认 CloseReason/USER_STOP 与回收。）

**判定：✅ 与手册一致。**

### M05（同进程 app 重建 R3）

- 驱动 Job `core-flow-restart-1ca8f03-r3`：**Complete**（15m）；`source_commit = 1ca8f03…`
- native execution `8116e457-3e06-4fe3-97c3-88de0ae90076`
- `Publication.ID = bdf26c96-cd74-59a8-bef9-82b273c70b62`（重建后**仍完成发布**）✅
- `ComputeCleanup.phase = APPLIED`；`StepExitCleanup` 存在（RunID/WorkflowUID 记录）
- DB 该行 `close_reason = STEP_FAILED`、有 `closed_at`、有 `publication_id`
- 前序轮 `core-flow-restart-1ca8f03`、`-r2` 为 Error（迭代失败轮），`-r3` 为最终通过轮

**判定：✅ 与手册一致（R3 通过）。**

### M06（新 Pod/进程接手，训练完成后 Stop）

- 驱动 Job `core-flow-close-late-stop-1ca8f03-r2`：**Complete**（90s）；evidence `core-flow-evidence-close-late-1ca8f03-r2`
- native：execution `b4031b61-9b8e-4ef4-98fa-f37417d8c227`、`scope = CORE_FLOW_CLOSE_ONLY`、`source_commit = 1ca8f03…`
- DB：`close_reason = USER_STOP`、`close_requested_at = 2026-10-15 09:15:16.683327+00`、`closed_at = 2026-10-15 09:19:26.937915+00`
- **时间序核对**：手册称训练 Complete `09:14:51Z` < Stop `09:15:16.683327Z` → 与 DB `close_requested_at` **逐字一致** ✅
- `Publication = None`（close-only 不产生新发布）✅

**判定：✅ 与手册一致。**

### M07（训练中原 deadline 到期，r2）

- 驱动 Job `core-flow-deadline-1ca8f03-r2`：**Complete**(5m14s)；evidence `core-flow-evidence-deadline-1ca8f03-r2`
- 前序 r1（`core-flow-deadline-1ca8f03-r1`）为 Error —— 对应手册"r1 是训练前到期失败，不能替代 r2"✅
- native：execution `801f27e7-c8e3-40aa-83fb-1e3cce72d618`、`source_commit = 1ca8f03…`
- `CloseReason = DEADLINE`、`CloseRequestedAt = 2026-10-15T09:43:11.640417Z`、`ClosedAt = 2026-10-15T09:43:44.681144Z`
  → **与手册 §8 的 `CloseRequested 09:43:11.640417Z` 逐字一致** ✅
- `Publication = None`、`FinetuningSteps` 为空（**无 export/verify 新许可、无发布**）✅
- DB：`publication_id` 为空、`close_reason = DEADLINE`
- 原 `ani-kfp-workspace-801f27e7…`（20Gi, ani-cephfs）：**状态 Bound，保留** ✅
- 对照 `923cf386…` 也是 `DEADLINE`（更早一轮）

**判定：✅ 与手册一致。**

### M08（正式 webhook 与临时依赖错误）

- `ani-modeldev-webhook` Deployment：**generation 8 / observedGeneration 8 / readyReplicas 2** ✅
- EndpointSlice：2 个 Endpoint 均 `ready=true / serving=true / terminating=false`（分别落 ani-02 / ani-03）✅
- TENANT 内 `Role ani-modeldev-webhook`：rules = `[{apiGroups:[argoproj.io], resources:[workflows], verbs:[get]}]`
  → **已恢复为原始窄规则** ✅（对应手册"临时限制 Role 以演练依赖失败后恢复原 rules"）
- 历史 Env 证据（`ENV-webhook-*`、`ENV-restart-1ca8f03-serverdryrun-20-actual`）属 Fedora/.scratch，**未取得**

**判定：✅ 集群侧可见部分与手册一致；演练 rollout/role-restore 原始 JSON 标 NOT_VERIFIED。**

### M09（参数/幂等/清理的 CODE + 真实 PG 回归）

- 手册称"CODE/真实 PG 验证通过 + `make verify` 门禁通过"。
- 集群侧**无可复核对象**（属 Fedora 编译与测试产物）。
- **判定：NOT_VERIFIED（本机无法复核）。**

---

## 4. 需组长澄清的差异（3 处）

以下不影响 M3-A 通过的定性，但**手册/交接的记录口径与集群实测不一致**，建议对齐后再对外引用。

### 4.1 driver 镜像 digest 不一致

- 手册 §2：`FT_DRIVER_IMAGE=localhost/ani-ft-core-driver@sha256:9be4c9ab0daed5beccbb0ad2988f94c60c7b7a2fd138f86274ef96c7b0c3c0a4`
- 实测 A/B 驱动 Job **spec.image**：`localhost/ani-ft-core-driver@sha256:db5714fe8d9426b6e5af4dd4435bbbcc1e37ea9602b734febf8a704baeceee72`
- 说明：Pod `status.imageID` 显示 `docker.io/library/import-2026-10-15@sha256:e68eb428…`——localhost 镜像 import 后重新哈希，与源 manifest digest 天然不同（此点正常）。**但 Job 请求的源 digest 与手册写的 `9be4c9ab…` 不同**。
- **请组长确认**：是手册记录过时，还是 A/B 轮实际用了另一个镜像？若是后者，应按"换镜像须重新冻结绑定"的口径重述。

### 4.2 source-commit 存在 4 个取值，手册"单一基线"表述与证据不符

实测各轮 `ani.modeldev/source-commit` 注解：

| 轮次 | source-commit |
| --- | --- |
| A / B / readback | `f7b852e6242608bcade9a2b1676971390bdc848f` |
| Stop | `9c8ab030ca839176b71e18fc63d137484d5b5bab` |
| restart / deadline / late-stop | `1ca8f031dab70cfd702edb9c277ca6de8c0897db` |

已用 `git merge-base --is-ancestor` 验证：**三者都是 `origin/main`（@`ee46246`）的祖先**（均 exit 0），提交说明分别为：
- `f7b852e` — Honor explicit streaming budgets while awaiting storage commits
- `1ca8f03` — fix: retain cleanup proof after explicit Pod admission denial
- `9c8ab03` — test: wait for complete durable training observations

问题：手册 §2 把 `1ca8f03` 立为"实际业务 driver 源码"基线，而 **A/B 主链实际记的是 `f7b852e`**。属迭代过程正常，但**"单一基线"的表述会误导后续接手**，建议改为"按轮次分别记录实际 source-commit"。

### 4.3 kubeconfig 入口不同

- 手册 §2 预期 API：`https://172.16.101.10:6443`
- 本机 context（`ani-lab`）：`https://lb.kubesphere.local:6443`
- cluster UID 相同、节点相同，属**不同 kubeconfig 入口**，非环境变更。记录备查即可。

---

## 5. 无法在本机复核（Fedora 侧，标 NOT_VERIFIED）

`.scratch`、Fedora 原始材料、`.private` 不随 Git 分发。以下均**未取得、未复核**，本报告不据此下任何"通过"结论：

| 类别 | 具体材料 |
| --- | --- |
| 原始记录 | `.scratch/finetuning-cpu/records/*`（各 `actual-summary-terminal.json`、`original-native.json`、`original-workflow*.json`、`failure-actual.json`、`FINAL-environment-actual-20261015.json` 等） |
| M03 工具 | `$FT_BASE/workload/readback-s15-v3/{job.py,readback.py,receipt.json,review.json}` |
| M06 工具 | `$FT_BASE/workload/close-late-stop-1ca8f03-r2/{prepare.py,plan-review.json,suspend-review.json,ready.json}` |
| M04/M07 工具 | observer_v2.py、`evidence-a-f7b852e-fetch/`、`workload/direct-s15/observer_v2.py`、atomic-deadline 脚本 |
| M08 记录 | `ENV-webhook-848d50b`、`ENV-restart-1ca8f03-serverdryrun-20-actual`、`ENV-webhook-role-restored` |
| 台账 | `.private/driver-state.json`、`candidate-release.json`、`storage-receipt.json`、`foundation-*.json`、`pipeline-receipt.json` |
| M09 门禁 | `make verify` 与 `go test ./contract/cpup01 …` 的退出码与 receipt |
| 历史保留 | a16（ComputeCleanup `BLOCKED/attempts=3`）、b7（`TRAINJOB_CREATE_UNRESOLVED`）、R2（no-route 网络 UNKNOWN）的原始实际 |

集群侧可见的对应痕迹（可佐证这些材料"曾存在并被消费"）：
- `core-flow-stop-374ac79-ts74m`、`core-flow-a-native-hp7jw` 等 Pod 为 **Failed**（历史失败轮）
- `modeldev_close_intents` 中 a16（`a16a03ee…`）、b7（`b7b52879…`）均为 `USER_STOP / CLOSING`，与手册"历史未解决事实单列保留"方向一致
- `modeldev_execution_cleanup_audits` 当前 **0 行**——注意：与手册 §3.9 所述 CPU-P01 清理审计不同表/不同库，**本库无清理审计行**，不构成矛盾，但后续若要核 M09 清理收据需在 Fedora 取。

---

## 6. 复核方法与可复现命令（只读）

> 前置：本机默认出口不可达，须用 WLAN 源地址。

```powershell
# 0) 通道自检（预期 OK / ani-01）
python local-remote\ttyd-exec.py --quiet --source-ip 192.168.203.132 "echo OK; hostname"

# 1) 身份读回
python local-remote\ttyd-exec.py --quiet --source-ip 192.168.203.132 -c "kubectl get ns ani-ft-core-20261014 -o jsonpath='{.metadata.uid}'"

# 2) A/B 驱动 Job 与 evidence
python local-remote\ttyd-exec.py --quiet --source-ip 192.168.203.132 -c "kubectl -n ani-ft-core-system-20261014 get job core-flow-a-f7b852e"

# 3) A native（evidence 卷日志）
python local-remote\ttyd-exec.py --quiet --source-ip 192.168.203.132 -c "kubectl -n ani-ft-core-system-20261014 logs core-flow-evidence-a-f7b852e-5dds7 --all-containers=true"

# 4) 真实 PG 状态投影
python local-remote\ttyd-exec.py --quiet --source-ip 192.168.203.132 -c "kubectl -n ani-ft-core-system-20261014 exec deployment/core-flow-postgresql -- psql -X -A -F'|' -U finetuning_owner -d finetuning_core_flow -c \"select execution_id,close_reason,close_requested_at,closed_at,publication_id from modeldev_execution_runtimes order by close_requested_at nulls last\""
```

> 注意：`ttyd-exec.py` 的 `-c` 参数对含空格/管道的复杂命令容易被 argparse 拆分；复杂命令建议写入 `--script` 文件按行执行（heredoc 会挂起）。

---

## 7. 结论与建议

1. **M3-A 集群侧证据成立**：手册 M01–M08 的主链结论，在 K8s 对象、Evidence 卷 native JSON、真实 PostgreSQL 三层均可独立复核，未发现伪造或相互矛盾。
2. **3 处差异需与组长对齐**（§4），其中 §4.1（driver 镜像 digest）与 §4.2（source-commit 口径）建议**修订手册**，避免后续接手误判"当前绑定的镜像/源码"。
3. **Fedora 侧材料未取得**（§5），凡依赖它们的断言（M09 门禁、M03/M06/M07 helper 原始记录、webhook 演练 rollout）一律 `NOT_VERIFIED`。若要把复核从"集群侧"升级为"全量"，**需组长受控转运**：`$FT_BASE` 任务包（尤其 `.private`）、`.scratch/finetuning-cpu/records/*`、三个 driver/component/train 镜像的可用镜像源。
4. **接手 P0 的前置仍缺**（不在本报告范围）：真实 Model 的 API/身份/存储交接合同（Model owner 提供）、PVC 容量/有限配额授权（环境 owner 提供）。二者到位前，P0 无法在集群上落地。
5. **本报告不改变 M3-A 的既有结论**，也不对 MR / FT29 / A10 / SCHEME_FINAL 下任何"通过"判定——与手册 §1 的口径一致。
