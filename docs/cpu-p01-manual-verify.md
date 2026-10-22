# CPU-P01 训练验证记录（新 CSV 全链路）

更新日期：2026-10-10。面向验收与运维人员。

对应文档：[手动部署与当前状态](cpu-p01-manual-deploy.md)、[手动复部署与业务测试](cpu-p01-manual-test.md)。

本文记录 2026-10-09 至 2026-10-10 在 `ani-system` 用**全新合成的 CSV** 跑通 CPU-P01 正向业务链与反例矩阵的实际结果，并如实标注尚未执行的部分。所有结论以集群实测为准，不包含登录凭据与签名 URL。

## 1. 完成度总览

| 阶段 | 命令 | 结果 |
| --- | --- | --- |
| 租户对齐核对 | BFF 查询 | ✅ presets=4 / input-versions=5 / executions=23 |
| 启用成功预设 | `t enable success` | ✅ Release 导入 + 绑定启用（generation=1） |
| 登记新输入 | `admin modeldev-import-csv` | ✅ 新 CSV → READY（1024×16） |
| 提交执行（幂等） | `t run bprime-02 success` | ✅ 受理 + 重放一致 |
| 等待完成 | `t wait bprime-02 SUCCEEDED` | ✅ SUCCEEDED / PUBLISHED / CLOSED |
| 独立加载验证 | `t verify bprime-02` | ✅ **L4_PASS** |
| 第二次成功训练 | `t run/wait/verify bprime-03` | ✅ SUCCEEDED / PUBLISHED / CLOSED + **L4_PASS** |
| 反例（隔离/幂等/未授权） | `t negatives bprime-03` | ✅ **PASS**（7 项断言全绿） |
| 失败路径复演 | `t run/wait/inspect failure-01 fail` | ✅ FAILED / CLOSED / `close_reason=STEP_FAILED` |
| 停止路径复演 | `t run/inspect stop-01 stop` | ✅ CLOSED / `close_reason=USER_STOP` |
| 截止时间路径复演 | `t run/wait/inspect deadline-01 deadline` | ⏭️ **跳过**（根因已定位为产品侧，本次不修；见 §5.5） |
| 正式清理 | `t cleanup-plan/apply bprime-03` | ✅ **APPLIED**（targets 已删除、PVC/发布保留、清理后重验 **L4_PASS**） |

> 结论：**真实训练、四文件发布、普通 BFF 下载、独立 CPU 加载的正向链已完整跑通并复演一次；手册 §7 反例矩阵（幂等改意图、跨租户隔离、未授权、无 token）亦全部通过；手册 §8 的失败（fail）与停止（stop）路径已通过**。手册 §8 的**截止时间（deadline）路径经研判为产品侧关闭驱动的健壮性缺口**（执行卡在 `CLOSING`，详见 §5.5），**本次决定不做修复、按跳过处理**，留待产品侧立项处理；§9 的正式清理已在 `bprime-03` 上执行并 **APPLIED**（见 §3.9），清理后 S3 发布仍可独立下载加载（**L4_PASS**）。

## 2. 关键事实

| 项 | 值 |
| --- | --- |
| 执行 ID | `f8aa5902-7c77-4b59-8b3a-3dcd1df8ff8d` |
| Operation ID | `b7fc379c-d0ce-4bb6-8536-955f4e54b561` |
| 解析 Release | `fabc4827-165c-4946-aeef-e5a16997dee8`（digest `53c73c02…a61`） |
| 输入版本 | `1cb6b8d9-a108-4194-b4f2-b56723584d35`（1024 行 / 16 维，READY） |
| 输入对象 | `ani-cpu-p01-input-d606e28a` / `inputs/fixed/data-bprime-20261009/data.csv` |
| 对象版本 | `99f575a3-9107-46e3-a6c5-ead786e10df8`（size 157724，sha256 `d1e660bd…398f`） |
| KFP Workflow | `general-cpu-wkfq4`（Succeeded） |
| 训练参数 | epochs=3 / batch_size=64 / learning_rate=0.01 |

第二次训练（复演，`bprime-03`）：

| 项 | 值 |
| --- | --- |
| 执行 ID | `7af805a8-5370-4dff-92b1-24a8568d16ac` |
| Operation ID | `7c59e676-9881-4538-a245-4215d54b2508` |
| 解析 Release | `fabc4827-165c-4946-aeef-e5a16997dee8`（同一 Release） |
| 输入版本 | `1cb6b8d9-a108-4194-b4f2-b56723584d35`（同一 READY 输入） |
| TrainJob | `md-7af805a8-5370-4dff-92b1-24a8568d16ac`（jobset `Complete`） |
| 训练参数 | epochs=3 / batch_size=64 / learning_rate=0.01 |

## 3. 执行结果

### 3.1 状态机

```
compute_state   = SUCCEEDED
delivery_state  = PUBLISHED
close_state     = CLOSED
```

`t wait` 最终输出 `{"result":"PASS","expected_compute":"SUCCEEDED"}`。

### 3.2 独立加载（`t verify`）

独立 verifier Job **不挂载原训练 PVC、不持有 S3 key、不自动挂载 SA token**，经 BFF 授权下载并核验四个产物：

| 文件 | 字节 | sha256 |
| --- | --- | --- |
| `model_config.json` | 116 | `1286ff35…e0f9` |
| `model.pt` | 4903 | `730424bb…67d7` |
| `summary.json` | 357 | `d67f7413…e035` |
| `metrics.jsonl` | 7236 | `78b53233…1c36` |

verifier 严格加载真实 PyTorch 权重并做 CPU 前向，输出：

```json
{"event":"L4_PASS","execution_id":"f8aa5902-...","shape":[4,2],"device":"cpu","epochs":3,"optimizer_steps":48,"artifact_count":4}
```

`48` 次 optimizer update 与合同（3 epoch × 64 batch × 1024 样本）一致。

### 3.3 发布产物（对象存储）

执行完成后，产物已发布到对象存储 `ani-cpu-p01-artifact-d606e28a`，前缀为租户与该执行：

```
executions/46936e6b-3a3b-4868-8d88-6833665abb70/f8aa5902-7c77-4b59-8b3a-3dcd1df8ff8d/
├── model.pt              4903
├── model_config.json      116
├── metrics.jsonl         7236
├── summary.json           357
└── output-manifest.json  1228   ← 发布清单/收据
```

| 对象 | 说明 |
| --- | --- |
| `model.pt` / `model_config.json` | 模型权重与结构 |
| `metrics.jsonl` / `summary.json` | 训练指标与汇总 |
| `output-manifest.json` | 发布清单：登记四文件的引用与输入摘要，是「四文件已发布」的凭证 |

> `PUBLISHED` 的物证即上述对象存在；`t verify` 通过 BFF 下载授权取得前四个文件并逐一核对字节与 SHA256（见 §3.2）。

### 3.4 第二次训练（复演）

以**新执行名** `bprime-03` 在同一预设、同一 READY 输入上再次提交，验证结果可复演、非偶然：

- 提交 `POST /executions` 返回 `202`，且**同键重放**返回同一 `execution_id`/`operation_id` 且 `replayed=true`（幂等 PASS）；
- 状态机推进：`SUBMISSION_CONFIRMED → TRAINING → SUCCEEDED`，随后 `delivery_state=PUBLISHED`、`close_state=CLOSING → CLOSED`；
- `t wait` 输出 `{"result":"PASS","expected_compute":"SUCCEEDED"}`；
- 独立 verifier 再次下载四文件并严格加载：

```json
{"event":"L4_PASS","execution_id":"7af805a8-...","shape":[4,2],"device":"cpu","epochs":3,"optimizer_steps":48,"artifact_count":4}
```

`48` 次 optimizer update 与合同一致（3 epoch × 64 batch × 1024 样本），与首次训练完全相同，说明链路稳定可复现。

### 3.5 反例矩阵（`t negatives`）

在第二次执行 `bprime-03` 上执行反例矩阵，覆盖幂等、参数校验、租户隔离、越权与未认证五类拒绝路径，**7 项断言全部符合预期**：

| 断言 | 期望 | 实测 | 语义 |
| --- | --- | --- | --- |
| `same-key-changed-name` | 409 | `IDEMPOTENCY_CONFLICT` | 同幂等键但改名 → 拒绝（幂等键绑定原意图） |
| `wrong-epochs` | 400 | `INVALID_MODELDEV_CREATE` | 非法训练参数（epochs=4 越界）→ 拒绝 |
| `gpu-kind` | 400 | `INVALID_MODELDEV_CREATE` | CPU 预设提交 GPU 类型 → 拒绝 |
| `b-execution` | 404 | `RESOURCE_NOT_FOUND` | **跨租户隔离**：租户 B 看不到租户 A 的执行 |
| `b-artifact` | 404 | `RESOURCE_NOT_FOUND` | **跨租户隔离**：租户 B 取不到租户 A 的产物内容 |
| `ungranted-execution` | 403 | `FORBIDDEN` | **同租户越权**：无 ModelDev 授权用户被拒 |
| `no-token` | 401 | `401` | 未携带令牌 → 未认证 |

> 该矩阵证明：执行与产物按租户强隔离（跨租户返回 404 而非泄露），权限按用户模型判定（无授权返回 403），幂等与参数边界按合同拒绝。至此手册 §7 反例矩阵在真实环境通过。

### 3.6 失败路径（`fail`）

在 `fail` 预设上提交 `failure-01`（执行 `d35e12cf-464b-4a8e-ba14-297f2c97c575`），训练程序按失败配方运行至非零退出：

| 项 | 实测 |
| --- | --- |
| `compute_state` | `FAILED` |
| `compute_outcome` | `FAILED` |
| `delivery_state` | `PENDING`（**未发布任何产物**） |
| `close_state` | `CLOSED` |
| `close_reason` | `STEP_FAILED` |
| 训练容器 | `exit_code=1` |
| 控制器对象 | TrainJob/JobSet/Job `terminal=true` |

结论：失败由训练程序真实非零退出驱动，关闭原因与退出证据一致，且未产出发布物。

### 3.7 停止路径（`stop`）

在 `stop` 预设上提交 `stop-01`（执行 `d1eeccb0-6221-4f35-93fb-542ca305e79d`）。客户端在**观察到真实 optimizer 输出（step 1、2…）后**立即发送 Stop 并重放：

| 项 | 实测 |
| --- | --- |
| `close_state` | `CLOSED` |
| `close_reason` | `USER_STOP` |
| `stop_requested` | `true` |
| `close_generation` | `1` |
| Stop 回执 | `intent_generation=1`，重放返回同 generation 且 `replayed=true` |
| 步证据 | 真实 `ani.metric.v1` / `train.loss`，step ≥ 3 且 < 48 |
| 训练容器 | `exit_code=143`（SIGTERM） |
| 控制器对象 | TrainJob/JobSet/Job `creation_disabled=true`（已取消，无运行中写者） |

结论：Stop 由客户端主动意图驱动，同一 Operation、同一 intent generation 保持，关闭原因 `USER_STOP`，且停止后未发布完整模型。

### 3.8 截止时间路径（`deadline`）— ⏭️ 跳过（产品侧待立项）

在 `deadline` 预设上提交 `deadline-01`（执行 `72155ea2-fa77-43da-a87e-79ee8877c794`，Release 原始 `execution_timeout_seconds=60`）。执行在截止时间后进入 `close_state=CLOSING`，但**持续 15 分钟以上未能收敛到 `CLOSED`**：

| 项 | 实测 |
| --- | --- |
| `compute_state` | `SUBMISSION_CONFIRMED`（未推进） |
| `close_state` | `CLOSING`（长时间不收敛） |
| `close_generation` | `1` |
| KFP Workflow | `general-cpu-jr9b5` → `Failed`（`deletionTimestamp` 为**空**，未被删除） |
| 关闭步骤 | `finalize-close` 调 `RequestExecutionClose` 返回 **401 `managed workload authentication failed`** |
| Governance 日志 | 反复 `WARN modeldev close claim unavailable` |

**根因（已定位，经集群复核）**：见 §5.5。要点是该 workflow 带 **workflow 级 `activeDeadlineSeconds=0`**（截止时刻＝创建时刻 `07:07:12`）。正文组件 `workspace-name` 的 executor 节点 `...-663707367` 在创建 Pod 前就已超时，于 `07:08:17` 直接 `Pending -> Failed`（消息 `Step exceeded its deadline`，无任何 `Created pod:` 记录）。此后该 Workflow 的**历史节点图里保留了这个 Pod 类型节点，但从未存在对应 Pod 对象**。ModelDev 的关闭/身份校验（`resolvedManagedTasks` / `VerifyClosingRun` / `VerifyOwnerWritersAbsent`）要求 Run 的每个受管任务都能在**实际 Pod 列表**中解析出唯一 Pod，缺失即判 `ErrRuntimeNotReady`，因此 `finalize-close` 无法完成关闭（401），独立 CloseWorker 也长期不收敛（执行停在 `CLOSING`）。

> 纠正早期记录：先前把原因写成「Argo 删除 workflow 使对象带 `deletionTimestamp`」。实测 workflow `deletionTimestamp` 为空、`finalize-close` Pod 仍存在，故该解释不成立，已按上述证据改写。

**归属与处置**：研判为 **(a) 产品侧关闭驱动的健壮性缺口**（见 §5.5）。修复需放宽关闭证据对「Argo 已记录但从未落地的 executor 节点」的容忍规则，涉及写者缺失这一安全不变量，**本次决定不改代码**，将该路径记为**跳过**，交产品侧另行立项；`execute_timeout_seconds=60` 是手册 §8 明确的 Release 契约属性，故**不做 (b) 放宽超时**。

### 3.9 正式清理（`cleanup-plan` / `cleanup-apply`）— ✅ APPLIED

对已关闭的 `bprime-03`（执行 `7af805a8-5370-4dff-92b1-24a8568d16ac`，`compute=SUCCEEDED / delivery=PUBLISHED / close=CLOSED`）执行手册 §9 的清理：

| 步骤 | 命令 | 实测 |
| --- | --- | --- |
| 生成计划 | `t cleanup-plan bprime-03` | 返回 `plan_sha256=cd0d232b…66da6`，3 个 `terminal=true` 目标：TrainJob / JobSet / Job `md-7af805a8-…` |
| 保留范围 | （plan `retained`） | `PVC`、`Workflow`、`Pod`、`S3 publication`、`execution and audit records` |
| 执行清理 | `t cleanup-apply bprime-03` | `phase=APPLIED`；3 个目标全部 `confirmed_absent` |
| 清理后核对 | kubectl 复查 | 3 个目标对象 **UID 均不存在**；workspace PVC `ani-kfp-workspace-7af805a8-…` **仍在**；Workflow `general-cpu-6dmjv` 保留 |
| 清理后重验 | `cp receipt → t verify bprime-03-after-cleanup` | **L4_PASS**（四文件下载校验通过、`epochs=3`、`optimizer_steps=48`） |

结论：清理**只删已关闭执行的计算资源**（TrainJob/JobSet/Job），**保留** workspace PVC、Workflow、Pod 与 S3 发布；清理后 S3 发布产物仍可脱离训练卷独立下载并加载，证明「发布与计算解耦」的保留语义成立。

> 说明：清理目标必须**已 `CLOSED`**。`deadline-01`（`72155ea2`，卡在 `CLOSING`）会被 `ErrCleanupBlocked` 拒绝（[execution_operations.go](../internal/biz/execution_operations.go) 要求 `Close=CLOSED`、`ClosedAt!=nil`、`CloseEvidence!=nil`、`CloseReviewReason==""`），故本次未纳入清理，待产品侧修复其关闭路径后另行处置。

## 4. 验证流程与验收标准

本节说明本次实际执行的步骤、每步的入口与通过判据，便于复现与核对。

### 4.1 流程总览

```
用户登录(BFF) → 启用预设(enable) → 登记输入(import-csv)
     → 提交执行(run，幂等) → 观察至关闭(wait) → 独立加载验证(verify)
```

全程经 **Governance BFF** 的 `/admin/v1/modeldev/*` 用户接口发起，管理动作（导入 Release / 登记 CSV / 启用绑定）经 Governance admin CLI 以 mTLS 调 ModelDev；普通用户不直连 ModelDev、不参与对象存储授权。

### 4.2 各步骤与通过判据

| 步骤 | 入口 | 通过判据 |
| --- | --- | --- |
| 登录 | `POST /api/v1/auth/password/login` | 返回 access_token，且其权限声明含 `modeldev:*` |
| 启用预设 | `enable success`（admin `modeldev-import-release` + `modeldev-enable`） | Release 导入成功；绑定 `generation=1`、`new_submissions_enabled=true` |
| 登记输入 | admin `modeldev-import-csv` | 状态 `READY`，`row_count=1024`、`feature_count=16` |
| 提交执行 | `POST /executions`（`202`） | 返回 execution/operation；**同键重放**返回同一对 ID 且 `replayed=true` |
| 观察完成 | `GET /executions/{id}` 轮询 | `compute_state=SUCCEEDED`、`delivery_state=PUBLISHED`、`close_state=CLOSED` |
| 独立加载 | verifier Job（`verify`） | 四文件字节/SHA 校验一致；严格加载权重、CPU 前向输出有限 → `L4_PASS` |

### 4.3 训练合同比对（实测 vs 约定）

| 合同项 | 约定 | 实测 |
| --- | --- | --- |
| 类型 | `GENERAL_TRAINING`，CPU 单节点 | 一致 |
| 输入 | CSV，`x0..x15,label`，1024 行 × 16 维 | 一致（`1cb6b8d9`） |
| 网络 | MLP `16 → 32 → 2` | verifier 前向输出 `[4,2]` |
| 训练 | Adam，3 epoch，batch 64，48 次 update | `epochs=3`，`optimizer_steps=48` |
| 产物 | `model.pt`/`model_config.json`/`metrics.jsonl`/`summary.json` | 四文件齐全且校验通过 |

### 4.4 覆盖与未覆盖

- **本次覆盖**：单租户（tenant-a）的完整正向链（授权受理 → 冻结 → KFP 训练 → 四文件发布 → BFF 授权下载 → 脱离训练卷的独立 CPU 加载），正向链**复演一次**（`bprime-03`）；反例矩阵全部 7 项（幂等改意图、非法参数、跨租户隔离、未授权、无 token）；非成功路径中的**失败（fail）**与**停止（stop）**两项。
- **本次未覆盖 / 未通过**：**截止时间（deadline）路径在真实环境未能收敛**（卡在 `CLOSING`，见 §3.8、§5.5），经研判为**产品侧缺口，本次跳过、不改代码**。正式清理已在 `bprime-03` 上完成（§3.9）。详见 [§6 待办](#6-尚未执行--待办)。

### 4.5 与手册的对应

本流程对应 [手动复部署与测试手册](cpu-p01-manual-test.md) §5（`login → catalogue → enable`）与 §6（`run → wait → logs → inspect → verify`）。本次以**全新合成的 CSV** 执行，替代手册中复用既有 READY 输入的做法，其余步骤与判据与手册一致。

## 5. 走通本流程所做的修复与发现

这些修复位于部署包的测试脚手架/集群配置中，非 modeldev 业务代码；其中 §5.5 为已定位的产品侧缺口（本次跳过，留待产品侧立项）。

### 5.1 `test.py` 的 admin 镜像与配置源

现象一：`manage()` 用在线 `ani-governance` server 镜像，该镜像入口为 `/app/bin/server`，**无 `/app/bin/admin`** → Pod `StartError`。

修复：`manage()` 改用含 admin 的镜像 `172.16.101.10:5000/ani-governance-cpu-p01:live-20261005-05`（`imagePullPolicy: IfNotPresent`）。该镜像 admin 支持 `modeldev-import-release / modeldev-import-csv / modeldev-enable`。

现象二：admin 报 `modeldev management requires a valid tenant user access token and session`。根因是 admin Job 挂载单数 Secret `governance-config`（旧库 `ani_cpu_p01_governance` + HS256），而在线 governance 用复数 Secret `governance-configs`（新库 `ani_governance` + RS256）。

修复：manner Job 的 `config` 卷改用 `governance-configs`，initContainer 改为复制整个配置目录（而非仅 `bootstrap.json`）。

### 5.2 `release-imports.json` 与已部署 facts 对齐

现象：`t enable success` 的 admin 返回 503 `query unavailable`（modeldev 侧 `ImportRelease` 查询失败）。

根因：`release-imports.json` 是重新 render 生成的，其 release_id 与**已部署的** `md-fixed-config` facts / `modeldev-manual-releases` catalogue 不一致。

修复：以已部署 catalogue 的 canonical Release 重建 `release-imports.json`，四个 mode 的 digest 与 facts 逐一核对一致：

| mode | release_id | 已部署 facts digest（一致） |
| --- | --- | --- |
| success | `fabc4827-165c-4946-aeef-e5a16997dee8` | `53c73c02…` |
| fail | `cf61932f-59e1-42ed-86eb-7912a9373a5a` | `7f066d72…` |
| stop | `3db624c8-e83a-40d4-af72-e5102d27d09a` | `1767ce78…` |
| deadline | `cf914145-4d68-430d-bdda-49343e97b9e2` | `a43b1f0c…` |

### 5.3 exit-retention webhook 超时（**必须固化，否则复现会卡**）

现象：首次执行 `b79fb009` 训练计算 `SUCCEEDED`，但 `close` 阶段 Pod 被拒：

```
failed calling webhook "step-exit-retention.modeldev.ani.io":
Post "https://ani-modeldev-exit-retention.ani-system.svc:443/mutate?timeout=2s": context deadline exceeded
```

工作流整体 `Error`，执行卡在 `delivery_state=PENDING / close_state=OPEN`。

根因：`MutatingWebhookConfiguration ani-modeldev-exit-retention` 的 `timeoutSeconds: 2` 对"每次冷建 TLS 且握手串行化"的 Python 后端（`/app/retention.py`）太紧；早前的执行只因抖动才未命中。

修复（保持 `failurePolicy: Fail`）：

```bash
kubectl patch mutatingwebhookconfiguration ani-modeldev-exit-retention --type=json \
  -p '[{"op":"replace","path":"/webhooks/0/timeoutSeconds","value":10}]'
```

> ⚠️ **固化提醒**：`manual.py render` 从 seed 重建 webhook 时**不覆盖 `timeoutSeconds`**，任何重新 render/deploy 都会把 2s 打回。需把本 patch 记入部署流程（改 seed 或加 patch 步骤），否则下次复现会重新卡在 close。
>
> 该 webhook 作用域极窄（仅 `ani-kfp-manual-a` 内由 argo 创建、带 KFP v2 标签的步骤 Pod），改超时不影响控制面。

### 5.4 `test.py negatives` 的 `UnboundLocalError`

现象：执行 `t negatives` 立即报 `STOPPED: UnboundLocalError`。

根因：`negatives()` 内有一句函数级 `import urllib.error`，使 `urllib` 在整个函数作用域被当作**局部名**；而同函数上一行的 `urllib.request.Request(...)` 在赋值前引用它，触发 `UnboundLocalError`。

修复：把 `urllib.error` 提到模块级 `import`，删除函数内的局部 import。修复后 `t negatives` 通过（见 §3.5）。

### 5.4b `test.py stop` 的调用契约与 `acceptance.py` 的响应形状

现象一：`t run stop-01 stop` 在提交后立即报错（先是 `KeyError`，先前为 `AttributeError`）。

根因一：`test.py` 的 stop 分支调用 `stop_training(api(), {'execution_id':…, 'operation_id':…})`，而 `acceptance.stop_training` 需要 `config['request']`（它自行重放请求、跟踪训练日志、再发送 Stop）。该分支此前从未被执行，契约不匹配未被暴露。

修复一：调用改为传 `{'request': body, 'execution_id':…, 'operation_id':…}`。

现象二：修复一后 `acceptance.stop_training` 报 `KeyError: 'execution'`。

根因二：BFF 的 `GET /executions/{id}` 返回**扁平** execution 对象（无 `execution` 包裹），而 `acceptance.py` 的 stop/follow/checks 路径仍按 `response['execution']` 读取。远端 `test.py wait` 已兼容两种形状，`acceptance.py` 未同步。

修复二：在 `acceptance.py` 增加 `execution_view()` 归一化（优先取 `response['execution']`，否则返回扁平响应本身），并替换三处读取。修复后 `t run stop-01 stop` 通过（见 §3.7）。

### 5.5 deadline 路径：Argo 未落地的 executor 节点使写者证据无法闭合（产品侧缺口，本次跳过）

现象：`t run deadline-01 deadline` 后执行卡在 `close_state=CLOSING` 超过 15 分钟，永不 `CLOSED`；KFP workflow `general-cpu-jr9b5` 最终 `Failed`，但 `deletionTimestamp` 为空。`finalize-close` 步骤调 `RequestExecutionClose` 返回 **401 `managed workload authentication failed`**；governance 侧同时反复 `WARN modeldev close claim unavailable`。ModelDev 独立 CloseWorker 虽持续扫描到该执行（DB `close_reason=DEADLINE`、`CloseGeneration=1`、`CloseRequestedAt=07:08:14`、`ClosedAt=null`），但同样无法收敛。

已定位的因果链（均以集群对象/日志复核）：

1. **该 workflow 带 `spec.activeDeadlineSeconds=0`**（截止时刻＝创建时刻 `07:07:12`）。正向成功的 workflow（如 `general-cpu-6dmjv`、`general-cpu-wkfq4`）**没有该字段**；失败路径的 `general-cpu-s9s6p` 也没有。该字段只出现在**被运行期终止的两个 workflow**：`general-cpu-8mppx`（stop）与 `general-cpu-jr9b5`（deadline）。workflow-controller 日志佐证：`retry exceeded workflow deadline 2026-10-10 07:07:12 +0000 UTC`。
2. **组件 Pod 因「节点截止」未获创建**：该 pipeline 的正文组件（`workspace-name`、`prepare`、…）运行在 `root.exit-handler-1` 的 DAG 内。controller 日志显示 `workspace-name` 的 executor 节点 `...-663707367` 于 `07:08:14` `initialized Pending`，`07:08:17` 直接 `phase Pending -> Failed`，消息 `Step exceeded its deadline`，全程**没有任何 `Created pod:` 记录**——即该节点在创建 Pod 前就已超时失败。（业务截止 `07:08:12`、workflow 截止 `07:07:12` 此时均已过；对照组 `stop` 的各组件 Pod 均在截止传导前已创建，故其历史节点无缺失。）
3. **历史节点图留下一个无 Pod 的 Pod 节点**：`...-663707367` 在 workflow 的 `status.nodes` 中保留为 `type=Pod, phase=Failed`（displayName `executor(0)`，属 `...workspace-name.executor`），且在节点字段上与「真正跑过」的节点可区分——**它没有 `hostNodeName`、没有 `outputs`、没有 `resourcesDuration`**，而所有实际落地的 Pod 节点都带 `hostNodeName=ani-0x`（Pod 由 `modeldev.ani.io/step-exit-evidence` finalizer 保留，节点与 Pod 一一对应）。**实际 Pod 列表里没有它的对象**（复核：6 个 `type=Pod` 节点中仅 5 个有 Pod，缺的正是 `663707367`）。对照 `stop` 的 `general-cpu-8mppx`：其节点无缺失，因关闭在截止回收之前完成。
4. **关闭证据要求每个受管任务可解析出唯一现存 Pod**：`resolvedManagedTasks`（[tasks.go](../internal/data/runtimeproof/tasks.go#L102-L108)）在 executor 节点数 ≠ 1 时返回 `ErrRuntimeNotReady`；`VerifyOwnerWritersAbsent`（[owner_close.go](../internal/data/runtimeproof/owner_close.go#L136-L139)）在 `len(seenNodes) != len(nodes)` 时同样返回 `ErrRuntimeNotReady`。缺失的 Pod 让「写者已停止」证据**永远无法重建**，于是 `finalize-close` 身份校验失败（401），执行停在 `CLOSING`。
5. **关闭意图本应由 ModelDev 独立驱动，但共用同一证据要求**：[execution_closer.go](../internal/biz/execution_closer.go#L56-L209) 的 `Reconcile` 与 [close_worker.go](../internal/biz/close_worker.go#L39-L74) 的 `ReconcileOnce` 本应在截止后独立关闭；[runtime.go](../cmd/ani-modeldev-service/runtime.go#L139-L140) 确认该 worker 在生产运行，[deadline_flow_test.go](../internal/data/submittest/deadline_flow_test.go) 亦断言 deadline 应自动关闭（`CloseReason=DEADLINE`、`ClosedAt!=nil`）。DB 实测该执行已被 worker 纳入：`close_reason=DEADLINE`、`CloseGeneration=1`、`CloseRequestedAt=07:08:14`、`ClosedAt=null`、`CloseReviewReason` 为空。但独立 worker 走的 `VerifyOwnerWritersAbsent` 与 workflow 内步骤**共用同一证据要求**，因此在节点无 Pod 时一起失效，长期停在 `CLOSING`。

> 纠正早期记录：先前写的「Argo 删除 workflow 使其带 `deletionTimestamp` → `stepidentity` 校验失败」**不成立**——实测 `general-cpu-jr9b5.deletionTimestamp` 为空、`finalize-close` Pod 仍在。真正的问题不在 workflow/Pod 被删除，而在 Argo 因 workflow 截止**从未创建** `workspace-name` 组件 Pod，留下一个无法解析的历史节点。

> 待确认：`activeDeadlineSeconds=0` 的**写入方**尚未定位。pipeline IR 只有**执行器级** `activeDeadlineSeconds`（600/1800），没有 workflow 级字段，故 `0` 不是 IR 直接产物；且 CreateRun 请求体只含 `runtime_config.parameters` 与 `pipeline_root`，未见 `max_run_duration`。已确认该字段**只伴随 workflow `:terminate`/截止终止出现**（stop 与 deadline 两个被终止的 workflow 都有，success/fail 都没有），倾向是 KFP 对「运行期被终止的 Run」的回写，而非本仓库部署/测试脚本的补丁。

**归属与处置**：研判为 **(a) 产品侧关闭驱动的健壮性缺口**——「历史 Pod 必须全部存在」的强证据要求，无法容纳 Argo 已记录但从未落地的 executor 节点（无写者存在）。正确修复是让关闭证据把「无 `hostNodeName` 的 Pod 类型节点」判为**从未落地＝无写者**，而不是缺失证据；但这直接触及「写者缺失」这一安全不变量（[owner_close.go](../internal/data/runtimeproof/owner_close.go#L14-L16) 注释明确要求独立观察写者缺席），**影响面与风险较高**。经决策：**本次不改代码**，将该路径记为**跳过**并留待产品侧立项；候选 **(b)** 放宽 deadline 预设的 `execution_timeout_seconds` 会违反手册 §8 明确的 Release 契约（60s），故**不采纳**。

本记录如实标注该路径**本次跳过、未通过**。


## 6. 尚未执行 / 待办

| # | 项 | 前置条件 |
| --- | --- | --- |
| 1 | **deadline 路径产品侧修复** | §5.5 已定位为产品侧关闭驱动缺口（容忍未落地的 executor 节点）。**本次不改代码**，留待产品侧立项；修复后需重跑 `deadline-01` |
| 2 | 正式清理 `t cleanup-plan/apply` | ✅ 已在 `bprime-03` 上 **APPLIED**（见 §3.9）；`72155ea2`（deadline-01）仍卡在 `CLOSING`，被 `ErrCleanupBlocked` 拒绝，待其关闭路径修复后另行处置 |
| 3 | `modeldev-mtls` 客户端证书换发 | **2026-10-12 到期**，到期后 governance→modeldev mTLS 失效 |
| 4 | webhook `timeoutSeconds` 固化 | 见 5.3 |
| 5 | `activeDeadlineSeconds=0` 写入方确认 | §5.5，倾向 KFP 对运行期被终止 Run 的回写，未最终定位 |

> 已完成的非成功路径：失败（fail，§3.6）、停止（stop，§3.7）。截止时间（deadline）路径经研判为产品侧缺口，本次**跳过**。

## 7. 复用命令（本次实际使用）

在 ani-01 部署包目录 `manual-ani-system-20261008/` 下：

```bash
# 1) 长生命周期 BFF 转发（前台或后台 setsid）
sudo -n kubectl -n ani-system port-forward --address 127.0.0.1 service/ani-governance 19778:7788

# 2) 登录三类身份（tenant-a admin / tenant-b / 同租户未授权用户）
#    凭据材料放在 .private/live-governance-20261005/（bootstrap-settings.json + *.password）
python3 test.py login

# 3) 启用 + 登记新输入（.private/bprime-import.request.json 指向新对象版本）
python3 test.py enable success
python3 -c "import test; test.manage('modeldev-import-csv','manual-import-csv-bprime', <request>)"

# 4) 预置 bprime-0x.request.json（dataset_version_id=新 input_version_id）后训练与验收
python3 test.py run    bprime-03 success
python3 test.py wait   bprime-03 SUCCEEDED
python3 test.py verify bprime-03

# 5) 反例矩阵（幂等改意图 / 非法参数 / 跨租户 / 越权 / 无 token）
python3 test.py negatives bprime-03

# 6) 非成功路径（逐个启用对应预设后提交）
python3 test.py enable fail
python3 test.py run    failure-01 fail
python3 test.py wait   failure-01 CLOSED
python3 test.py inspect failure-01        # 期望 close_reason=STEP_FAILED

python3 test.py enable stop
python3 test.py run    stop-01 stop       # 观察真实训练步后自动 Stop 并重放
python3 test.py inspect stop-01           # 期望 close_reason=USER_STOP

python3 test.py enable deadline
python3 test.py run    deadline-01 deadline
# ⚠️ deadline 路径本次跳过：72155ea2 卡在 CLOSING（见 §5.5），
#    产品侧修复后再执行下述 wait/inspect。
python3 test.py wait   deadline-01 CLOSED
python3 test.py inspect deadline-01       # 期望 close_reason=DEADLINE
```

## 8. 相关文件

- 部署入口：`scripts/cpu-p01-manual/manual.py`
- 业务测试：`scripts/cpu-p01-manual/test.py`
- 验收实现：`scripts/cpu-p01-manual/acceptance.py`
