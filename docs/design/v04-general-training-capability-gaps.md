# v0.4 通用训练能力缺口设计文档

- 文档定位：**面向 AI Agent 的可执行开发需求依据**。每个缺口给出功能边界、核心需求、验收标准、代码落点、依赖与建议顺序，可直接据此拆任务、写代码、写测试。
- 基准来源：`kubeflow文档/模型研发平台-v0.4/source/{architecture.md, development.md, operations.md}`（A06–A14、D02–D17、O06–O09）。
- 现状来源：本仓 `ani-modeldev-service` 与治理仓 `ani-governance` 的真实源码与设计文档（均已逐一核对，见各缺口"现状事实"）。
- 范围：**仅通用训练（GENERAL_TRAINING）**。受管微调、GPU、分布式、推理服务、Model 登记不在本文考虑范围内。
- 当前版本：2026-10-15（见 §9 修订记录）。
- 术语"CPU-P01" = 当前已实现并集群验收的第一条通用训练实现切片。

> **路径引用约定**：文中代码路径一律写作 `<仓名>/<仓内相对路径>`（如 `ani-modeldev-service/contract/cpup01/release.go`、`ani-governance/app/admin/...`）。**仓名是逻辑标识，不是本地目录**——各仓在不同工作区可能位于不同父目录，请按你本地的实际布局定位；未带仓名的路径（如 `internal/biz/`）默认指本仓 `ani-modeldev-service`。本文不写机器绝对路径与跨仓相对链接。

> **使用约定**：本文标记的"当前状态"是 **2026-10-15** 代码事实。开发前须以当时源码复核；凡本文标注"未实现"的能力，不得因本文存在而假称为已交付。

> **任务边界说明（2026-10-15）**：本文档仅服务**本期 CPU 通用训练流程**（v0.4 双流程中的 GENERAL_TRAINING 一条链）。受管微调由其他负责人承担，其权威正文为 `docs/design/ani-finetuning-v4.2.2/spec.md`（`finetuning-cpu.md` 已作废）；GPU/多框架等由 v0.4 显式标 DEFERRED，不在本期。

---

## 1. v0.4 通用训练能力完整清单（基准）

把 v0.4 分散在 A/D/O 三册中与"通用训练"相关的条目归并为 5 组、30 项（A9 + B8 + C7 + D5 + E1）。

### A. 用户侧自助能力

| 编号 | 能力 | v0.4 依据 |
|---|---|---|
| A1 | 小文件（≤32 MiB）自助上传：授权→直传暂存→固化→异步校验→READY | A07、D07 §7.1 |
| A2 | 输入版本列表/详情，按权限可见 | D04、A07 |
| A3 | 选择预设 + 允许参数 + 输入，提交训练（持久受理、幂等） | A08、D04 §4.1 |
| A4 | 执行列表/详情/进度步骤 | D04、D14 |
| A5 | 停止训练（持久意图→真实停止→CLOSED） | A12、D12 |
| A6 | 产物列表 + 短期授权下载 | D04、D11 |
| A7 | 重新训练：复制可见参数 + `source_execution_id` 关联重跑 | D04、A08 |
| A8 | 观测：受限 tail 日志、最终标量与一条曲线、步骤表、有界归档与缺口标明 | D14、D16-C27 |
| A9 | 受理暂停开关（`new_submissions_enabled`）：只阻新任务，不阻查询/原幂等重放/关闭 | D03、D04、O17、D16-C05 |

### B. 训练执行能力

| 编号 | 能力 | v0.4 依据 |
|---|---|---|
| B1 | 通用程序执行：运行**已登记镜像中的代码**，非写死某一模型 | A06、A08、D16-C15 |
| B2 | 输入格式：CSV 首例 + 通用 `RAW_FILE` 声明 | D07 §7.1 |
| B3 | 输出合同：固定"保存产物"（checkpoint + 配置 + 指标 + 清单，可下载） | A08、D11 |
| B4 | 单节点单进程 CPU，固定资源上/下限，无 GPU 字段 | A06、D08 |
| B5 | 每执行独立工作区，输入只读、输出唯一可写、执行间不覆盖 | D09 |
| B6 | 发布：真实文件持久化 + 远端字节核验 | A08、D11 |
| B7 | 关闭/停止/deadline/重启恢复：先持久意图再停算，创建围栏 | A12、D12 |
| B8 | 幂等受理 + 执行快照冻结（不随默认版本漂移） | D05、D13 |

### C. 管理能力

| 编号 | 能力 | v0.4 依据 |
|---|---|---|
| C1 | 环境/租户预开通（T01）；**含镜像空间预开通（`EnsureImageSpace`）+ namespace pull secret 预置**（训练镜像登记与拉取的前置） | D15、O03/O04 |
| C2 | 不可变目录导入 + CAS 启用/回退（T02） | D03、O08 |
| C3 | 训练镜像登记（固定 digest、入口、参数合同、输出合同）；**依赖镜像前置链路：`EnsureImageSpace`+`IssuePublisherCredential` 推入镜像空间，跨租户复用取 PLATFORM scope** | A05、O06 |
| C4 | 输入代导入（受管 S3，大文件）（T03） | A07、O07 |
| C5 | 执行只读检查 / 对账（T04） | D15、O12/O13 |
| C6 | 工作区/暂存受控清理 plan/apply（T06） | D15、O15 |
| C7 | 离线记录检查工具 `check_record.py`（T08） | D15、O02 |

### D. 安全

| 编号 | 能力 | v0.4 依据 |
|---|---|---|
| D1 | 租户/动作/数据范围授权，每次读/写按当前权限检查 | A11、D05 |
| D2 | 可信身份头，防伪造，原始 API 防旁路 | A11 |
| D3 | 多租户隔离，**至少两个租户负向验证** | A11、D16-C01 |
| D4 | 训练 Pod 无控制面/模型仓库凭据，不得越权读卷 | A11、D16-C04 |
| D5 | 受保护资产（首版基模/READY 输入/已登记输出）默认拒绝删除；引用检查完备前先禁用删除入口 | A10、D11、D16-C29 |

### E. 前端

| 编号 | 能力 | v0.4 依据 |
|---|---|---|
| E1 | 输入页、任务列表、创建表单、详情（阶段/日志/曲线/文件/停止） | D14 |

---

## 2. 逐项核查总表

图例：✅ 已实现并验收 ／ 🟡 部分实现（能力存在但边界不足） ／ ❌ 未实现。

| 编号 | 能力 | 状态 | 关键差距摘要 |
|---|---|---|---|
| A1 | 小文件自助上传 | ❌ | 无 upload 会话/预签名/固化/异步校验；输入只能管理员 CLI 导入 |
| A2 | 输入版本 List/Get | 🟡 | 查询 RPC 与路由存在，且 **List/Get 授权链路已接通**（`modeldev_authorization.go` L42-55、`modeldev_catalogue_service.go` L82-123）；能力已实现，尚缺跨租户负向的集群验收 |
| A3 | 提交训练（幂等受理） | ✅ | Governance 受理 + ModelDev 持久 ACK 已实现 |
| A4 | 执行列表/详情 | ✅ | 四轴状态 + `current_step` 已实现 |
| A5 | 停止 | ✅ | 持久意图 + 创建围栏 + 真实写者核对已实现 |
| A6 | 产物 + 下载 | ✅ | PUBLISHED + 短期签名下载已实现（60s 为当前实现口径，v0.4 仅要求"短期授权"） |
| A7 | 重新训练关联 | ❌ | `source_execution_id` 在 Governance 受理层被显式拒绝（403 SOURCE_EXECUTION_UNAVAILABLE）；ModelDev 已接受语法 |
| A8 | 日志 tail | ✅ | 受限 tail + Pod UID 核对已实现 |
| A8（续） | 指标曲线端点 | ❌ | v0.4 `GET .../metrics` 路由缺失；`ExecutionView.metrics` 未投影（注意 `internal/server/admin.go` 的 Prometheus `/metrics` 为运维端点，与执行指标无关） |
| A9 | 受理暂停开关 | 🟡 | `modeldev-pause`/`modeldev-enable` CLI 存在；"只阻新任务、不阻查询/原幂等重放/关闭"的边界语义与 D16-C05 验收未落实 |
| B1 | 通用程序执行 | ❌ | Release 只允许一个写死的 MLP 镜像/命令/参数合同；镜像需改走 `ani-resource-service` 镜像域（`img_*`），训练合同在 modeldev 侧登记（见 G-01） |
| B2 | 输入格式通用化 | ❌ | 仅 CSV（`ani.cpu.csv.v1`/1024/16），无 `RAW_FILE`（JSON/Alpaca 系微调输入，不属通用训练范围） |
| B3 | 输出合同 | 🟡 | 固定四文件可用；`create_tar_bundle` 字段存在但未启用通用 tar 打包 |
| B4 | 单节点单进程 CPU | ✅ | 资源与拓扑固定无 GPU |
| B5 | 工作区隔离 | ✅ | KFP `CreatePVC` 每执行独立工作区 |
| B6 | 发布字节核验 | ✅ | 远端长度/SHA 核验后 PUBLISHED |
| B7 | 关闭/停止/恢复 | ✅ | 已实现，含重启续跑关闭；deadline 驱动关闭（D08/D12）未单独验收 |
| B8 | 幂等 + 快照冻结 | ✅ | Intent/Spec 双摘要 + generation CAS |
| C1 | 环境/租户开通 | 🟡 | 能力与清单形态存在，逐环境物料仍需交付者生成 |
| C2 | 目录导入/启用/回退 | 🟡 | 导入/启用/暂停 CLI 存在；"回退演练"未验收 |
| C3 | 镜像登记 | ❌ | 无独立训练合同登记；当前镜像与命令被写死在 Release 校验中。**镜像本体复用镜像域（`ani-resource-service` 的 `img_*`），训练合同在 modeldev 侧登记**（见 G-01 设计路线 A2+T0）。**前置**：镜像须先 `EnsureImageSpace`+`IssuePublisherCredential` 推入镜像空间，跨租户复用取 PLATFORM scope |
| C4 | 输入代导入（T03 正式化） | 🟡 | 代导入能力已存在：业务方法 `ImportCSV`（`internal/service/materials.go` L73-91、`internal/biz/input_importer.go` L34-67）+ 管理 CLI `modeldev-import-csv`（`cmd/admin/modeldev_management.go` L157-158）。剩余为"命名正式化 + 大文件受管 S3 入口 + 授权/源范围校验" |
| C5 | inspect/reconcile | ✅ | CLI 存在 |
| C6 | 工作区清理 | 🟡 | 仅限 PUBLISHED + strict CLOSED + generation=1 的完整链 |
| C7 | 离线记录检查工具 | ❌ | 随包工具在文档包内，不在本仓交付链 |
| D1 | 当前权限授权 | ✅ | 每次查询/创建/停止重复授权 |
| D2 | 可信身份头 | ✅ | mTLS + 当前 Pod/Run 双向核验 |
| D3 | 多租户负向 | 🟡 | 设计选择要求 `ALL` 数据范围；两租户负向验证未全部验收 |
| D4 | 训练无控制凭据 | ✅ | Step 通过 STS 收窄权限，训练不挂长期 Secret |
| D5 | 受保护资产禁删 | ❌ | 删除类入口无受管阻断/引用检查；"现有删除入口不绕过受管资产"未验证 |
| E1 | 前端页面 | ❌ | 仅后端接口，前端尚未接入 |

---

## 3. 缺口详述（可执行需求）

以下每项为**独立可开发单元**。字段说明：
- **v0.4 依据**：需求出处。
- **现状事实**：当前代码/文档的具体状态。
- **功能边界**：必须做 / 明确不做。
- **核心需求**：可执行的分解。
- **验收标准**：可测、可判定的通过条件。
- **代码落点**：建议改动位置。
- **依赖**：前置缺口或外部条件。

---

### G-01 通用程序合同：镜像登记 + 参数/输入/输出合同去硬编码

- **优先级**：P0（这是"通用训练"成立的前提）
- **v0.4 依据**：A06、A08、D16-C15（"第二个已登记镜像复用同一条执行链，不改业务执行代码"）。
- **设计路线（2026-10-15 拍板：A2 + T0）**：训练镜像**复用 `ani-resource-service` 的镜像域**，不另造镜像仓库。
  - **A2（合同归属）**：镜像本体（repository/digest/purposes/accelerator/platforms）登记在镜像域，产出 `img_<32hex>`；command / 参数 / 输入 / 输出等**训练专属合同**由 modeldev 侧一条「镜像登记记录」承载，其镜像引用指向镜像域的 `img_*`。**不重复造镜像本体**，符合"每个服务只写自己负责的数据"。
  - **T0（解析时机＝发布期固化）**：镜像解析发生在**发布 / 导入 Release 时**（管理员登记镜像后一次性解析 `img_*` → `resolved_reference@digest`，**固化进不可变 Release**）。受理与运行期**完全不碰镜像域**：ModelDev 仍从 Release 读 `ImageDigest`，TrainJob 用 `snapshot.Program.ImageDigest`。好处：受理协议不变、保持"同一 Release → 同一 spec_hash"的确定性（避免受理时解析导致的 digest 漂移）。
  - **T0 具体调用的 RPC（明确）**：T0 在 Gov 发布链路使用**既有 client 的 `TenantImageService.GetImage`**（`image_client.go` 已封装，`image.registrations` 表持久化了 `digest` 与 `platforms`），据此读取登记 digest 与平台集，并在导入期校验含 `linux/amd64`。**不新增 svc→svc**：governance 全仓无 `ImageRuntimeService` client（`ResolveImageForWorkload`/`GetTenantPullMaterial` 零命中），本方案**不需要它**——平台匹配改用登记表已存的 `platforms` 字段即可，无需实时查 Harbor。故此调用属**既有 Gov→Image（TenantImageService）链路的一个新调用点**，须在接口登记留痕（规范见 `ani-governance/docs/contracts/service-to-service-calls.md`），但无新服务对、无新 RPC 权限。
  - **平台/架构校验取舍**：`RegisterImage` 经 `ResolveArtifact` 已把 `platforms` 固化进登记表（`harbor_artifact.go` `readRunnablePlatforms`），故 `GetImage` 即含平台信息；T0 在**导入期**据此拒绝不含 `linux/amd64` 的镜像，避免"登记成功但训练时 `ImagePullBackOff`"。若将来要求**实时** Harbor 复核或按平台选 manifest，则须新建 governance→`ImageRuntimeService` client（=新增 svc→svc，须登记）。
  - **schema 兼容**：Release 新增 `image_registration_id` 属 **schema 变更**，须同步更新 `contract/cpup01/conformance/release-v1.json` 等 fixture，并对**存量已导入 Release**给出兼容/迁移策略。
  - 否定方案（记录备查）：原拟"受理时由 Governance 解析并冻结（T1）"已否决——受理链现无镜像域调用，且受理时解析会使同一 Release 在不同时间产出不同 digest，破坏快照不漂移。
- **现状事实**：
  - `contract/cpup01/release.go` 中 `invalidReleaseDocument` 强校验 `command == ["/opt/venv/bin/python","-I","/opt/cpu03/train_mlp.py"]`、`args_template` 固定 5 对 flag/source（共 10 项），允许追加 `--recipe` 与 success/fail/slow-stop（至 12 项）、参数合同固定 `ani.cpu.mlp.parameters.v1`。
  - `contract/cpup01/intent.go` 的 `validateRegisteredParameters` 白名单固定 `epochs=3`、`batch_size=64`、`learning_rate (0,0.1]`，最多 3 个。
  - `contract/cpup01/snapshot.go` 的 CSV schema 固定 `ani.cpu.csv.v1`/1024 行/16 特征；输出四文件硬校验在 `contract/cpup01/release_parts.go` L49-63（`invalidOutputContract`，roles 固定四文件）。
  - **镜像域现状（复用目标）**：`ani-resource-service` 已有完整镜像仓库能力——`api/image/v1/image.proto` 的 `TenantImageService`（RegisterImage/GetImage/ListImages/UpdateImage/UnregisterImage）、`api/image/v1/runtime.proto` 的 `ImageRuntimeService.ResolveImageForWorkload`、`image.registrations` 表、Harbor 适配器、`img_*` 标识；`ImagePurpose` 已含 `TRAINING`/`FINETUNING`。治理侧已有 BFF 路由 `/api/v1/images/registrations`（`i_image.proto`）与出站 client `image_client.go`。
  - **镜像前置链路（硬前置，须先满足才能登记训练镜像）**：`ani-resource-service/internal/biz/image/catalog.go` L44 `RegisterImage` 会调 `ParseImageReference(space.RegistryAuthority, space.ProjectName, ref)`（实现 `validation.go` L61，要求引用**前三段中前两段严格等于该镜像空间的 authority/project**，L74/L77），并 `ResolveArtifact` 到 Harbor 解析真实 digest（L77-87）。故训练镜像**必须先 `EnsureImageSpace` + `IssuePublisherCredential` 推入租户/平台镜像空间**才能登记；跨租户复用同一镜像需 **PLATFORM scope**（`validateRegistrationOwner` L91 要求 `TenantID==""`）。
  - **断层（本缺口要弥合的）**：modeldev 的 `image_version_id`（Release 内 UUID）与镜像域的 `img_*` 是**两套独立命名空间，代码中无映射**；Release 里现用的 `registry.example.test/cpu-mlp@sha256:...` 只是**占位**。modeldev 侧无 resource 域 client（`internal/data/` 下有 KFP、K8s、S3 与本地读取器，但**确无** resource 域 client），从未调用过 resource。
  - **拉取凭据机制缺失（本轮识别的实质空白）**：训练镜像与 KFP 步骤镜像都是**私有镜像**，均需拉取凭据，但当前链路无正向机制承载：
    1. 快照身份字段无 pull secret：`contract/cpup01/snapshot.go` L91-97 的 `RuntimeIdentityRefs` 只有 5 个 SA/identity ref，**无 `imagePullSecret`**，快照无法承载"用哪个 secret 拉镜像"。
    2. Pod 模板未挂 secret：`internal/data/trainer/create.go` L168-175 的 `podTemplateOverrides` 设了 `serviceAccountName`/卷/挂载，**未设 `imagePullSecrets`**。
    3. 双私有镜像都需凭据，而文档此前只负向约束 Pod 不得拿 `GetTenantPullMaterial`（L166），无正向落点：`spec.trainer.image`（训练镜像）与 KFP 步骤镜像 `component_image`（`pipelines/cpu_p01.py` L14）是两个不同私有镜像。
    4. **时点缺失**：实例（`ani-resource-service`）是"按需建 pull secret"，而训练链路**没有该时点**——须在 **T01 / 租户预开通阶段**于训练 namespace 预置拉取 secret，C1 此前只加了 `EnsureImageSpace`，未含此项。
    5. **模板哈希约束**：`create.go` L235 `safeRuntime` 对 Runtime 模板 spec 做 SHA256 强校验（须等于 `snapshot.Release.Runtime.ContentSHA256`）。若把 `imagePullSecrets` 写进 Runtime 模板，则等于改模板 = 须**重发 Release**；故**优先**验证能否经 `podTemplateOverrides` **pod 级**注入 `imagePullSecrets`（避免动模板哈希）。
- **功能边界**：
  - 做：允许管理员登记**任意符合条件的单进程 CPU 训练程序**；镜像本体走镜像域（`img_*`），训练合同（固定 command + 参数白名单 + 输入格式声明 + 输出合同）在 modeldev 侧登记；通过 Gov 受理解析后复用同一 KFP/Trainer 执行链。
  - 不做：不做代码包上传、不做在线构建、不做运行时装依赖、不做多框架/分布式、不做任意 shell 插值、**不在 modeldev 另建镜像仓库**。
- **核心需求**：
  1. 新增 modeldev 侧**镜像登记记录**（不可变）：`image_registration_id`、**镜像域引用 `image_id`（`img_*`）+ `image_scope`**、解析后的 `image_digest`（manifest digest，禁浮 tag）、固定 `command`、`parameter_contract`（参数名/类型/必填/默认/min/max/allowed）、`input_contract`（格式：CSV-v1 或 RAW_FILE）、`output_contract`（required_files）、资源上限。
  2. **发布期解析与固化（T0）**：导入 Release 时（或登记镜像时）**一次性**调镜像域解析该 `img_*` 为 `resolved_reference@digest`，写进 **Release 的 `program.image_digest`** 后固化（**Release 不可覆盖安装由 `internal/data/catalogue/importer.go` L72 的 `RENAME_NOREPLACE` 保证**，非 reader.go）。受理与运行期**不回读镜像域**；`ResolveAdmissionRequest` 不加镜像字段，ModelDev 仍从 Release 读 `ImageDigest`（`admission_resolution.go` L203 不变），TrainJob 用 `snapshot.Program.ImageDigest`（`trainer/create.go` L164 不变）。
  3. **schema 变更与迁移**：Release 新增 `image_registration_id`（及可能的 img_* 引用结构）属 schema 变更，须同步更新 `contract/cpup01/conformance/release-v1.json` 等 fixture，并对**存量已导入 Release**给出兼容/迁移策略（如：旧 Release 缺字段时按占位 digest 继续可读、新导入强制解析）。
  4. `ReleaseDocument` 的 `program` 从"仅 MLP"改为"引用 modeldev 侧镜像登记记录"，`ParseRelease` 只校验结构一致性，不再硬编码具体程序。
  5. 参数校验由"写死白名单"改为"按登记的 parameter_contract 校验"：保留按名称排序、拒绝重复、值一律字符串、类型匹配。
  6. 首例 MLP 作为**其中一个已登记镜像**保持行为不变（回归不破）。
  7. **镜像拉取凭据（正向机制）**：为训练镜像与 KFP 步骤镜像提供私有拉取凭据并挂到 Pod——(a) 租户预开通（T01）阶段在训练 namespace 预置 pull secret；(b) 快照/计划承载"用哪个 secret"的引用（`RuntimeIdentityRefs` 增 `image_pull_secret` 或经 pod 级 override）；(c) `create.go` 的 pod 模板注入 `imagePullSecrets`；(d) 验证 `Trainer v2.1` 是否支持 **pod 级** `imagePullSecrets` override（避免因改 Runtime 模板哈希而须重发 Release）；(e) KFP 步骤镜像的凭据同样落地，不可只覆盖训练镜像。
- **验收标准**：
  1. 在镜像域登记新镜像 + 在 modeldev 登记其训练合同后，不改动 ModelDev 业务执行代码即可创建并跑通一次通用训练（映射 D16-C15）。
  2. **仅导入期校验**镜像有效性：导入 Release 时 `img_*` 不存在/未 ready/digest 缺失或浮 tag 一律拒绝；**运行期不做该镜像域校验**（T0 不碰镜像域，故发布后 unregister 不拦截）。
  3. 参数超出登记白名单或类型不符被拒绝（400/`INVALID_ARGUMENT`）。
  4. 首例 MLP 执行链全部既有测试保持 GREEN（含 fixture 迁移后）。
  5. **第二镜像（C15）端到端可拉取**：在仅含平台私有镜像的环境下，训练镜像与 KFP 步骤镜像均能凭预置 secret 拉取成功，无 `ImagePullBackOff`；且未破坏 `safeRuntime` 模板哈希校验（未重发 Release）。
- **代码落点**：`contract/cpup01/release.go`、`intent.go`、`snapshot.go`（去硬编码 + 镜像登记引用 + `RuntimeIdentityRefs` 增 pull secret 引用）、`internal/data/trainer/create.go`（pod 级注入 `imagePullSecrets`）、`internal/data/catalogue/`（在既有 Release 目录导入上扩展：导入时经 `GetImage` 解析 digest 并固化 + 平台校验，勿另建平行目录）、`api/ani/modeldev/v1/`、`pipelines/cpu_p01.py`（KFP 步骤镜像凭据）、`ani-governance/app/admin/service/internal/data/image_client.go`（**发布/导入链路**复用既有 `GetImage`；非受理链路）、`ani-governance/app/admin/service/cmd/admin/modeldev*.go`（新增训练合同登记 / 导入时解析固化的 CLI）、租户预开通链路（T01，预置 namespace pull secret）。
- **跨服务约束**：T0 在 Gov 侧**发布/导入链路**新增一个调用点（`TenantImageService.GetImage`），属既有 Gov→Image 链路，**无新服务对、无新 RPC 权限**，但须在接口登记留痕。若将来改用 `ImageRuntimeService.ResolveImageForWorkload`（须新建 governance 侧 `ImageRuntimeService` client = **新增 svc→svc**），须按 `docs/contracts/service-to-service-calls.md` 逐条登记；训练 Pod 不得获得 `GetTenantPullMaterial` 等**镜像域下发凭据**（Pod 侧拉取改用 T01 预置的 namespace secret，机制见核心需求第 7 条）。
- **依赖**：无（可与 G-03 并行开工；G-02 经 G-03 间接依赖本项，故 G-02 需待 G-01/G-03 之后）。
  - **跨仓硬前置（镜像前置链路）**：训练镜像登记前须先完成 **`EnsureImageSpace` + `IssuePublisherCredential`**（推入租户/平台镜像空间），否则 `RegisterImage` 因 `ParseImageReference` 前两段不匹配而拒绝；跨租户复用需 PLATFORM scope（详见「现状事实」镜像前置链路）。该前置应纳入 **C1（环境/租户预开通）**，并在 **C15（第二镜像）** 验收中体现。
  - **跨仓硬前置（拉取凭据预置）**：训练 namespace 的 pull secret 须在 **T01 / 租户预开通阶段预置**（训练链路无"按需建 secret"的时点），该前置同样纳入 **C1**，否则 C15 端到端拉取失败。KFP 步骤镜像与训练镜像的凭据都要覆盖。
  - 镜像域登记解析能力已具备，无需本缺口新造；本缺口新增的是 modeldev 侧训练合同登记 + 发布期解析固化 + 拉取凭据落地。

---

### G-02 用户小文件自助上传闭环

- **优先级**：P0（v0.4 A07 明确"日常小文件上传不需要管理员每次审批"）
- **v0.4 依据**：A07、D07 §7.1（状态机 `UPLOADING→FREEZING→VALIDATING→READY`）。
- **现状事实**：
  - `ani-governance/api` 与 `ani-modeldev-service/api` 中**无任何 upload 相关路由/RPC**（已 grep 确认。注：`modeldev.proto` L647-694 的 `upload_task_id`/`UploadCompletionEvidence` 为内部运行证据字段，非用户上传接口，勿混淆）。
  - `ani-governance/docs/frontend-cpu-training-handoff.md` §9 明确"浏览器上传 CSV 不支持"。
  - 后端仅有管理员 CLI `modeldev-import-csv`。
- **功能边界**：
  - 做：≤32 MiB 单次直传；后端生成写入位置；固化后再校验（顺序强制）；异步校验；READY/REJECTED 终态；迟到 PUT 不改变 READY 内容。
  - 不做：用户侧 multipart/断点续传（本版删除）；外部连接管理；对象目录浏览。
- **核心需求**：
  1. 新增路由（**路由语义对齐 v0.4 D04 的 upload 四步；前缀沿用本仓实际 `/api/v1/modeldev/`**，非 D04 原文的 `/admin/v1/modeldev/`）：`POST /api/v1/modeldev/uploads`（CreateUpload）、`POST /api/v1/modeldev/uploads/{id}:authorize`（续签，仅未完成有效会话）、`POST /api/v1/modeldev/uploads/{id}:complete`（固化+异步校验）、`POST /api/v1/modeldev/uploads/{id}:abort`（关闭暂存，迟到写入不晋级）。
  2. 持久状态机字段合并进输入记录：`staging_ref`、`fixed_ref`、`declared_bytes`、`actual_bytes`、`sha256`、`expiry`、`state`、`failure_code`。
  3. **固化再校验**：先固化到平台独占写权限的固定 key，再读取实长/SHA/格式；固定位置不得再向浏览器签发 PUT。重复 `Complete` 只指向同一固定对象。
  4. 32 MiB 上限在 **BFF 元数据**与**实际对象**两处检查。
  5. 输入版本 List/Get 的**当前授权链路**接通（与 G-06 的 T03 共用输入服务）。
  6. 错误码遵循 D04：复用保留错误清单（`INVALID_ARGUMENT`、`IDEMPOTENCY_CONFLICT`、`NO_COMPATIBLE_RELEASE`、`ENVIRONMENT_NOT_READY`、`UPSTREAM_UNAVAILABLE` 等），新增有界错误 `INPUT_TOO_LARGE`、`CAPABILITY_NOT_ENABLED`、`MANUAL_REVIEW_REQUIRED`；对象隐藏策略不得泄露其他租户存在性。
  7. 兜底签名遵循 D07 §7.1：短有效期、限制会话数；过期后迟到上传不可能晋级 READY。
- **验收标准**：
  1. 直接上传小 CSV 后自动固化/校验为 READY，可被 CreateExecution 使用（映射 D16-C06）。
  2. 超 32 MiB、错格式、迟到 PUT 一律拒绝；READY 正式内容不被旧签名覆盖（映射 D16-C07）。
  3. 失败返回有限 error code，不自动清洗数据。
  4. 校验由程序完成，浏览器无法直接置 READY。
- **代码落点**：`ani-modeldev-service/api/ani/modeldev/v1/modeldev.proto`（新增 upload 消息/RPC）、`internal/service/`、`internal/biz/`、`internal/data/input/`（复用现有 `InputImporter`/`RecordVerifiedCSV` 语义）、`ani-governance/api/protos/admin/service/v1/i_modeldev.proto`（新增 HTTP 路由）、`internal/data/modeldev_*_client.go`。
- **依赖**：G-03（格式通用化，若要支持 `RAW_FILE`）；G-06（与代导入共用输入服务）。

---

### G-03 输入/输出合同通用化（RAW_FILE、多格式、通用输出）

- **优先级**：P0
- **v0.4 依据**：D07 §7.1（"一般已登记镜像可声明 RAW_FILE，仅做固定内容和上限检查，算法语义由其程序负责"）、D11。
- **现状事实**：
  - `VerifyCSV` 固定 `ani.cpu.csv.v1`、1024 行、16 特征、二分类。
  - 输出 `required_files` 固定 `model.pt/model_config.json/metrics.jsonl/summary.json`（硬校验落点在 `contract/cpup01/release_parts.go` L49-63 `invalidOutputContract`）。
- **功能边界**：
  - 做：CSV-v1 首例保留；新增 `RAW_FILE`（仅固定内容+上限检查，不解释算法语义）；按登记声明选择校验器；输出合同可由 Release 声明（不写死四文件）。
  - 不做：任意格式自动识别、自动清洗、丢弃样本。
- **核心需求**：
  1. 输入校验器按 `input_contract.format` 分派：`CSV-v1` 走现有校验；`RAW_FILE` 仅校验 UTF-8/大小/非空/上限。
  2. 格式校验**不执行用户代码**。
  3. `OutputContract` 允许登记声明 required_files（路径/role/限额），快照校验改为"与登记一致"而非"等于四文件"。
- **验收标准**：
  1. 登记 `RAW_FILE` 的镜像上传普通文件即 READY，训练程序自行解释。
  2. 输出合同声明任意合法文件集合时，collect/publish 按其校验。
  3. 首例 MLP 四文件合同回归 GREEN。
- **代码落点**：`internal/data/objectstore/`（新增 verifier 分派）、`contract/cpup01/snapshot.go`、`release.go`、`internal/data/workspace/collector.go`。
- **依赖**：G-01。

---

### G-04 重新训练（Rerun 关联）

- **优先级**：P1
- **v0.4 依据**：D04（"重新训练由前端复制可见参数，再调用 CreateExecution，使用新幂等键及 `source_execution_id`"）。
- **现状事实**：`intent.go` 接受 `source_execution_id` 语法（`intent.go` L41/L126）；`403 SOURCE_EXECUTION_UNAVAILABLE` 由 ani-governance 受理层返回（`app/admin/service/internal/service/modeldev_service.go` L117-119；见 `ani-governance/docs/frontend-cpu-training-handoff.md` §4.3）。
- **功能边界**：
  - 做：`source_execution_id` 作为**关联**参与意图哈希与受理；须校验可访问；**不强制复用旧 Release**（用当前启用版本）。
  - 不做：不做 Rerun 管理域、不做强制复现旧快照。
- **核心需求**：
  1. 受理时校验 `source_execution_id` 属于当前租户且可访问，否则 400/403。
  2. 该字段参与 `intent_hash`（presence 语义与其它可选字段一致）。
  3. 详情视图回显 `source_execution_id`。
- **验收标准**：
  1. 携带合法 `source_execution_id` + 新幂等键可创建新执行，且新执行使用当前启用 Release。
  2. 不可访问/跨租户的 source id 被拒绝。
  3. 同键同意图重放返回原执行。
- **代码落点**：`ani-governance/app/admin/service/internal/service/modeldev_service.go`（当前 403 拒绝点，首要改动）、modeldev `internal/biz/`、`internal/data/submission/`、`internal/service/query.go`。
- **依赖**：无。

---

### G-05 指标端点、曲线与日志有界归档

- **优先级**：P1
- **v0.4 依据**：D14（"结束后读 Metrics 与 JSONL，点数上限，区分最终平均与最后一步"；归档行）、D04（`GET .../metrics`）、D16-C27（失败日志：明确错误与有界日志，日志不可恢复时显示缺口）。
- **现状事实**：
  - `modeldev.proto` 的 `ExecutionView.metrics` 字段存在，但 `internal/service/query.go` 只投影 `current_step`，未投影 metrics。
  - BFF 无 `/metrics` 路由。
  - `metrics.jsonl` 作为 artifact 文件已发布。
- **功能边界**：
  - 做：提供执行级最终标量 + 受限点数序列（一条曲线）；无指标则如实显示"未提供"。
  - 不做：WebSocket/SSE 推送、复杂游标恢复、多运行对比。
- **核心需求**：
  1. 新增 `GET /api/v1/modeldev/executions/{id}/metrics`，返回最终标量与序列（点数上限）。
  2. 从已发布 `metrics.jsonl` 读取，区分"最终平均"与"最后一步"。
  3. 无指标不得伪造曲线。
  4. 失败执行的日志访问有界：明确错误与有界日志；日志不可恢复（如 Pod 已回收）时如实显示缺口，不伪造（D14 归档行、D16-C27）。
- **验收标准**：
  1. 训练成功后可取到 metrics，点数不超过上限。
  2. 无指标文件时返回明确"未提供"，不返回空伪造。
  3. 未 PUBLISHED 时按现有错误折叠规则返回 412。
  4. 日志不可恢复时返回明确缺口标记而非空内容或报错堆栈（映射 C27）。
- **代码落点**：`api/ani/modeldev/v1/modeldev.proto`（新增 RPC）、`internal/service/query.go`（投影）、`internal/data/workspace/`（metrics 解析）、`internal/data/traininglogs/`（有界读取与缺口标记）、governance `i_modeldev.proto` + `modeldev_query_http.go`。
- **依赖**：无。

---

### G-06 输入代导入（T03）正式化 + 输入 List/Get 授权

- **优先级**：P1
- **v0.4 依据**：A07、D07 §7.2、O07、D15-T03。
- **现状事实**：
  - **List/GetInputVersion 授权链路已接通**：`ani-governance/app/admin/service/internal/data/modeldev_authorization.go` L42-43（`modelDevGetInputVersionAction`/`modelDevListInputVersionsAction`）、L54-55（路径常量）、L75-78 分派；BFF 侧 `internal/service/modeldev_catalogue_service.go` L82-123 均先做 `modelDevQueryScope` 再委派。
  - **代导入能力已存在（非空白）**：冻结→校验→READY/REJECTED 由 `internal/biz/input_importer.go` L34-67 与 `internal/service/materials.go` L73-91 的 `ImportCSV` 承载；管理入口为 CLI `modeldev-import-csv`（`cmd/admin/modeldev_management.go` L157-158）；治理侧委派客户端含 request-id 幂等派生（`internal/data/modeldev_management_client.go` L49/L87/L106）。注意方法名为 `ImportCSV`，无独立命名的 `CreateInputImport`。
- **功能边界**：
  - 做：受管 S3 对象代导入（登记 storage_connection_id、获准源 key、类型、最大长度）；复用同一固化/校验；不设连接管理页。
  - 不做：任意公网 URL 拉取、目录浏览 UI、用户无限上传。
- **核心需求**：
  1. 将既有 `ImportCSV` 链路正式化为受管代导入入口（可沿用现名或收敛命名），复用输入服务（禁止在脚本里复制一套身份/校验）。
  2. List/GetInputVersion 授权**已满足**，仅需在回归中确认（跨租户不可见）。
  3. 源对象版本固定（有版本用版本，无版本生成不可覆盖正式副本）。
  4. 遵循 D07 §7.2：导入请求携带 `request_id` 幂等；失败按原对象核对后重试，不无限创建临时副本。
- **验收标准**：
  1. 管理员经受管入口导入大文件后生成 READY 输入，源变更不改变已登记内容（映射 D16-C08）。
  2. 未授权来源/未知 endpoint 被拒绝。
  3. 普通用户在其权限范围内可见，跨租户不可见。
- **代码落点**：`ani-governance/app/admin/service/cmd/admin/modeldev*.go`、`ani-governance/app/admin/service/internal/data/modeldev_*_client.go`（注意客户端文件实际在治理仓 app 目录下）、modeldev `internal/service/materials.go`、`internal/biz/input_importer.go`、`internal/data/input/`。
- **依赖**：G-02（共用输入服务）、G-03（格式）。

---

### G-07 通用训练产物 tar 打包

- **优先级**：P2
- **v0.4 依据**：D11（"可打普通 tar 便于下载"）。
- **现状事实**：`OutputContract.CreateTarBundle` 字段存在，`PublicCandidate.BundleObject` 存在，但通用训练当前未启用打包。
- **功能边界**：
  - 做：产物可选打包为普通 tar 供下载；tar 摘要独立于文件摘要与 manifest 摘要。
  - 不做：不把 tar 当模型包登记（通用训练不登记 HF 模型）。
- **核心需求**：
  1. 按 `create_tar_bundle` 决定是否产出 bundle object。
  2. 分别记录 `files[].sha256`、`manifest_sha256`、`bundle_sha256`。
- **验收标准**：
  1. 开启后下载可获取 tar，字节核验通过。
  2. 三个摘要互不混用。
- **代码落点**：`internal/component/io.go`、`internal/data/workspace/`、`internal/biz/`（publication）。
- **依赖**：G-03。

---

### G-08 前端对接需求（后端侧保障，非前端界面设计）

- **优先级**：P1
- **v0.4 依据**：D14、E1。
- **定位说明**：本节只定义**后端为前端对接所需保障的东西**（接口、字段、契约稳定性、错误语义、与后端缺口的派生关系），**不写前端界面设计**（布局/组件/交互属前端仓，权威对接合同见 `ani-governance/docs/frontend-cpu-training-handoff.md`）。这样设计的原因：前端要渲染的每一项都受后端缺口约束，把"页面 → 后端依赖"钉在这里，才能判断后端缺口是否齐备、是否越界。
- **现状事实**：后端 10 条路由已就绪（全部 `/api/v1/modeldev/*`，见下表）；前端未接入。

#### 8.1 页面 → 后端接口对应表（已有 / 待建）

| 页面 / 能力 | 依赖后端接口 | 状态 |
|---|---|---|
| 预设下拉、参数表单 | `GET /api/v1/modeldev/presets` | ✅ 已有 |
| 输入数据集下拉、详情 | `GET /api/v1/modeldev/input-versions[/{id}]` | ✅ 已有（授权链路已接通） |
| **输入上传（小文件）** | `POST /api/v1/modeldev/uploads`、`:authorize`、`:complete`、`:abort` | ❌ **待建（G-02）** |
| 提交训练、重训（复制参数） | `POST /api/v1/modeldev/executions`（含 `source_execution_id`） | 🟡 已有接口，**重训字段待打通（G-04）** |
| 任务列表、详情（轮询） | `GET /api/v1/modeldev/executions[/{id}]` | ✅ 已有 |
| 运行日志 | `GET /api/v1/modeldev/executions/{id}/logs` | ✅ 已有（503 容错已定义） |
| **指标曲线** | `GET /api/v1/modeldev/executions/{id}/metrics` | ❌ **待建（G-05）** |
| 产物列表、下载 | `GET .../artifacts`、`GET /api/v1/modeldev/artifacts/{id}/content` | ✅ 已有 |
| 停止 | `POST /api/v1/modeldev/executions/{id}:stop` | ✅ 已有 |

#### 8.2 前端能力 ↔ 后端缺口的派生关系（决定排期）

- **上传页** → 依赖 **G-02**（无上传接口则无法自助上传）；G-02 又依赖 G-03 的格式分派。
- **参数表单动态渲染** → 依赖 **G-01** 产出的 `parameter_contract`（`parameter_rules` 不得写死）。
- **曲线展示** → 依赖 **G-05**（无 metrics 端点则只能显示"未提供"）。
- **重训按钮** → 依赖 **G-04**（`source_execution_id` 打通前，复制参数重提会被 403）。
- 即：**G-08 的验收不能早于 G-02/G-04/G-05 完成**；G-01/G-03 决定表单与产物渲染是否可信。

#### 8.3 后端必须保障的对接契约（本项的后端工作）

1. **接口稳定**：上表已就绪的 10 条接口，路径/请求/响应结构在本次迭代内保持不变（如确需变更，须同步更新 handoff 文档与 §2 契约）。
2. **字段可驱动渲染**：`preset.kind`、`parameter_rules`（含 `null` 判定）、产物 `filename`/`role`、四轴状态枚举，均须由后端返回、前端不硬编码。
3. **错误语义明确**：创建 412 的 `reason`（`INPUT_NOT_READY`/`NO_COMPATIBLE_RELEASE`/`SUBMISSIONS_PAUSED`）、日志 503、下载 60s 过期，须与 handoff §6 一致。
4. **不越界**：不向前端暴露 tenant/namespace/Pod/object key 等可信字段（请求体与 query 均不得出现）。
- **功能边界**：
  - 做：输入选择/上传、任务列表、创建表单、详情（阶段表/日志/曲线/文件/停止）的**后端支撑**。
  - 不做：自研 DAG、反向血缘全图、前端界面/组件设计。
- **核心需求**：
  1. 参数表单按 `parameter_rules` 动态渲染（不写死 epochs/batch/learning_rate）——依赖 G-01。
  2. `kind` 取自 `preset.kind`，不硬编码。
  3. 产物按 `filename`/`role` 渲染，不写死四文件名。
  4. 日志 503 容错、下载地址不缓存。
- **验收标准**：普通用户能完成"选数据→发起→观察→停止→下载"全链，无需管理员逐步介入（映射 D16-C32）。
- **代码落点**：后端保障落在 `ani-governance`（BFF 契约与路由）与 `ani-modeldev-service`（字段投影）；前端页面在**前端仓（本仓外）**，不在本文档范围。
- **依赖**：G-02（上传页）、G-04（重训）、G-05（曲线）；G-01/G-03（表单与产物渲染可信度）。

---

### G-09 多租户隔离负向验证与规模

- **优先级**：P2
- **v0.4 依据**：A11（"至少两个租户做负向验证"）、D16-C01/C02/C03。
- **现状事实**：当前静态受理 facts 限定每进程 1–64 个显式文件；dispatch binding 绑定单 tenant/environment；查询要求 `ALL` 数据范围；两租户负向未全部验收。
- **功能边界**：
  - 做：两租户正向/负向、跨租户详情/日志/下载/停止拒绝、伪造身份头拒绝。
  - 不做：一台实例的任意多租户调度（需先明确分片或受管 binding）。
- **核心需求**：按 D16-C01/C02/C03 逐条构造真实负向用例。
- **验收标准**：三组用例 PASS 且有真实证据。
- **代码落点**：测试与受管事实验证；如需扩展需明确 instance sharding。
- **依赖**：C1 环境多租户。

---

### G-10 受控清理通用化

- **优先级**：P2
- **v0.4 依据**：D15-T06、O15。
- **现状事实**：`modeldev-cleanup-plan/apply` 仅限"已 PUBLISHED + strict CLOSED + generation=1 且未 suspend/修改历史 spec"的完整链。
- **功能边界**：
  - 做：有界清理（精确 UID/RV、plan hash、审批、有效期、apply 复查）；活跃/未知/未发布阻断。
  - 不做：全平台自动 GC、引用图。
- **核心需求**：
  1. 扩展可清理判定，覆盖失败/停止工作区（在"无唯一产物、无写者、批准放弃"条件下）。
  2. 遵守 O15 推荐保留策略（管理默认、须环境记录批准）：过期暂存过期后留约 24 小时观察窗再核对；成功工作区至少保留 7 天；失败/停止 7 天；日志/运行证据暂按 30 天。
  3. 清理与受管保留/删除互斥，做不到互斥不 apply（O15）；正式工作区不得由 KFP 默认 GC 与清理器同时争抢。
- **验收标准**：映射 D16-C30；阻断条件严格生效。
- **代码落点**：`internal/data/cleanup/execution.go`、`internal/service/`。
- **依赖**：无。

---

### G-11 C7 离线记录检查工具入交付链

- **优先级**：P2
- **v0.4 依据**：D15-T08、O02。
- **现状事实**：`tools/check_record.py` 在文档包 `模型研发平台-v0.4/tools/`，不在本仓交付链。
- **功能边界**：仅检查管理记录 JSON 结构，不连集群、不证明事实。
- **核心需求**：把工具与 12 份模板纳入本仓交付物或在交付包明确提供。
- **验收标准**：对 `--allow-incomplete` / 正式两种模式行为符合 O02。
- **代码落点**：交付包/工具目录。
- **依赖**：无。

---

### G-12 受保护资产禁删（C29）

- **优先级**：P2
- **v0.4 依据**：A10（"首版基模、READY 输入和已登记模型不提供新删除能力；现有删除入口也必须保护这些受管资产。若尚无完备引用检查，**先禁用受保护资产的删除**，而不是只在手册里写'请勿删除'"）、D11 末段（"删除 RPC 须受管阻断/引用检查，否则关闭删除动作"）、D16-C29。
- **现状事实**：通用训练链内未新增删除 RPC；治理侧与对象存储适配层既有的删除类入口（清理 CLI、对象删除路径）未做受保护资产引用检查或阻断。开发前须以当时源码复核治理侧全部删除类入口清单。
- **功能边界**：
  - 做：对 READY 输入、首版基模、已登记输出/发布产物建立删除阻断或引用检查；引用检查不完备时先禁用删除动作。
  - 不做：引用图、自动 GC、批量删除工具（O15 明确本版不提供批量删除路径）。
- **核心需求**：
  1. 盘点治理侧与 modeldev 侧全部删除类入口（HTTP/CLI/作业）。
  2. 对受保护资产类别建立受管阻断或引用检查，至少覆盖 D16-C29 三类：当前输入、基模、已登记输出。
  3. 禁用而非"手册劝阻"：删除动作被关闭时返回有界错误并留记录，不静默放行。
- **验收标准**：映射 D16-C29——READY 输入、首版基模、已登记输出不可被任何现有删除入口绕过删除；尝试删除返回明确拒绝并留证据。
- **代码落点**：`ani-governance/app/admin/service/cmd/admin/`（清理/删除类 CLI 门控）、modeldev `internal/biz/`（publication/input 保护规则）、对象存储适配层。
- **依赖**：无（与 G-10 协同，注意保留/删除互斥）。

---

### G-13 受理暂停开关正式化（C05）

- **优先级**：P2
- **v0.4 依据**：D03（受理开关 `new_submissions_enabled` 与实际生效代际）、D04（`SUBMISSIONS_PAUSED`）、O17（"通过批准配置/受管工具关闭 new_submissions_enabled，逐实例验证。只阻止新任务；原幂等重放、查询、日志、内部步骤与关闭仍可运行"）、D16-C05。
- **现状事实**：治理侧已有 `modeldev-pause`/`modeldev-enable` CLI（`ani-governance/app/admin/service/cmd/admin/modeldev_management.go`），但"只阻止新任务；不阻止查询、原幂等重放、运行内部接口和关闭"的边界语义未落实为验收，D16-C05 未验收。
- **功能边界**：
  - 做：通过批准配置/受管工具关闭受理并逐实例验证；冻结特定输入或发布时阻止新引用，不静默修改旧快照。
  - 不做：按任务粒度的暂停；静默改写历史快照。
- **核心需求**：
  1. 暂停状态持久化，并与实际生效代际一致（D03）。
  2. 暂停期间 CreateExecution 返回 `SUBMISSIONS_PAUSED`；查询、原幂等键重放、日志、内部步骤与 Stop 不受影响。
  3. 逐 modeldev 实例验证生效，而非只改治理侧配置。
- **验收标准**：映射 D16-C05——暂停期间新任务拒绝；旧任务查询/原键重放/关闭全部正常；恢复后新任务正常且旧快照未被修改。
- **代码落点**：`ani-governance/app/admin/service/`（pause/enable 受管作业与生效验证）、modeldev `internal/biz/`（受理门控读取代际）、`internal/service/`。
- **依赖**：无。

---

## 4. 建议实施顺序

按"主链阻塞优先、依赖前置"排序：

```
阶段一（去硬编码，打通通用能力底座）
  跨仓前置：C1 完成 镜像空间(EnsureImageSpace) + namespace pull secret 预置  ← G-01 先决
  G-01 通用程序合同  ┐
  G-03 输入/输出合同  ┘ 并行 → 先固定 fixture 与合同测试

阶段二（用户自助，形成完整自助闭环）
  G-02 小文件上传（依赖 G-03 格式分派）
  G-06 代导入正式化 + 输入授权（与 G-02 共用输入服务）

阶段三（体验与完整性）
  G-04 重新训练
  G-05 指标曲线与日志归档
  G-08 前端对接（依赖 G-02/G-04/G-05；表单渲染依赖 G-01）

阶段四（加固与验收）
  G-07 tar 打包
  G-09 多租户负向
  G-10 清理通用化
  G-11 记录检查工具
  G-12 受保护资产禁删
  G-13 受理暂停开关
```

**验收推进顺序**（对齐 D17）：环境/合同 → 公共 CPU Trainer 短链 → 两条真实 CPU → 发布/仓库/独立推理 → 故障与手册演练。通用训练侧重点：C06/C07/C08（输入）、C15（第二镜像）、C09/C10（幂等/冻结）、C32（无人工串流程）。

---

## 5. 与 v0.4 D16 验收矩阵的映射

| 用例 | 对应缺口 | 当前 |
|---|---|---|
| C06 小 JSON/CSV 上传 | G-02 | ❌ |
| C07 超限/错格式/迟到 PUT | G-02、G-03 | ❌ |
| C08 受管 S3 代导入 | G-06 | 🟡 |
| C09 创建重放 | — | ✅ |
| C10 Release 切换与在途 | — | ✅ |
| C04 训练运行身份 | —（D4） | ✅ |
| C05 新受理暂停 | G-13 | 🟡 |
| C13 公共 Trainer | — | ✅ |
| C14 工作区 | — | ✅ |
| C15 第二个已登记镜像 | G-01 | ❌ |
| C16 CPU MLP | — | ✅ |
| C19 真实发布 | — | ✅ |
| C24 停止运行 | — | ✅ |
| C25 停止与迟到创建 | —（B7） | ✅ |
| C26 服务重启 | — | ✅ |
| C27 失败日志 | G-05 | 🟡（tail 有界已实现；缺口标记未验收） |
| C28 发布失败保留 | —（B7） | 待复核 |
| C29 受保护资产删除 | G-12 | ❌ |
| C30 受控清理 | G-10 | 🟡 |
| C32 无逐次人工串流程 | G-02、G-08 | ❌ |
| C01–C03 租户/身份负向 | G-09 | 🟡 |

（"✅"表示 CPU-P01 已在集群验收链中覆盖或等价满足；"🟡"表示能力部分存在但验收未闭环；"待复核"表示须按 D17 验收时以真实证据逐条回填，不得把 NOT_RUN 记为 PASS。注：C06 用例名为 D16 原文（JSON/CSV 双链共用上传入口），本文通用训练链实际只覆盖 CSV-v1 + `RAW_FILE`，JSON/Alpaca 属微调链。）

---

## 6. 明确不在本范围

微调闭环（A09/D10）、Model 登记、推理部署（O16）、GPU/分布式、浏览器 multipart 续传、代码包上传与在线构建、多框架、实时推送、自研 DAG、自动 GC、发布管理页面。这些由 v0.4 其它章节或后续增量处理，不得以本文档缺口为由提前实现或声称已完成。

> 微调（FINETUNING）由其他负责人承担，其权威正文为 `docs/design/ani-finetuning-v4.2.2/spec.md`（旧稿 `finetuning-cpu.md` 已作废）。本文档不覆盖、不阻塞微调链。

---

## 7. 使用本文件的方式（给 AI Agent）

1. 每个缺口 G-xx 是一个可独立领取的任务单元，含边界/需求/验收/落点/依赖。
2. 开发前**必须**先用 Read 复核"现状事实"中列出的文件，确认未被其它提交改变。
3. 实现遵循本仓 `AGENTS.md`：`internal/biz` 不依赖 transport/存储；只加纵向切片需要的适配器；提交前 `make verify`。
4. 每个缺口实现须先在**真实边界**取得 RED→GREEN 证据（本仓历史惯例），不得用模块 fake 冒充集群验收。
5. 完成后回填对应 D16 用例状态，不得把 NOT_RUN 记为 PASS。

---

## 8. 面向"本期 CPU 通用训练流程"的缺口分档

本文 §4 的建议顺序按"依赖前置"排列；本节改为**按"是否阻断本期 CPU 通用训练主流程"重新分档**，供排期直接取用。三档定义：**A = 不做则"通用程序可跑"不成立（把写死合同泛化为可配置任务的底座）；B = 主链可跑（管理员 CLI 代操作即可）但用户自助/可观测闭环不完整；C = 加固，不阻断流程。**

> 档位口径说明：CPU-P01 的**执行主链已验收**（管理员经 CLI 导入输入/Release 即可跑通训练）。因此 A 档只放"**不做则无法运行任意登记程序**"的项（G-01/G-03）；**面向普通用户的自助能力（如 G-02 上传）属 B 档**——缺它不影响主链跑通，只是普通用户无法自助。

> 前提认知：CPU-P01 的**执行底座已跑通**（幂等受理、快照冻结、四轴状态、五阶段 KFP、发布核验、停止/重启恢复）。因此"补全流程"的实质 = 把"写死 MLP 的固定合同"泛化为"用户可配置的通用任务"，并补用户自助入口，**不是从零搭链路**。

> 镜像路线（2026-10-15 拍板 A2+T0）：训练镜像**复用 `ani-resource-service` 镜像域**（`img_*` + Harbor + Resolve），**不在 modeldev 另造镜像仓库**；镜像解析在**发布/导入 Release 时**一次性完成并**固化进不可变 Release**，受理与运行期**不碰镜像域**（保持"同一 Release → 同一 spec_hash"确定性）。**注意**：T0 会在 Gov 发布链路上**新增一个调用点**（复用既有 Gov→Image client，非"零新增调用"）。详见 G-01。

### A. 主链阻断（本期通用训练流程成立的前提）

| 缺口 | 内容 | 现状 | 依赖 |
|---|---|---|---|
| **G-01** | 通用程序合同：镜像登记（复用镜像域 `img_*`）+ command/参数/输入/输出去硬编码 + **镜像拉取凭据落地** | ❌ 未实现（最核心） | 无（含 C1 跨仓前置：镜像空间 + namespace pull secret 预置） |
| **G-03** | 输入/输出合同通用化：`RAW_FILE` + 可变 `required_files` | ❌ 未实现（仅 CSV-v1、固定四文件） | G-01 |

> G-01 与 G-03 可并行开工（先固定 fixture 与合同测试）。二者共同构成"任意登记程序可跑"的底座。**G-01 依赖 C1 跨仓前置**（镜像空间预开通 + namespace pull secret 预置，见 G-01 依赖段），排期时勿漏。

### B. 闭环完整性（主链可跑后，用户自助与可观测所必需）

| 缺口 | 内容 | 现状 | 依赖 |
|---|---|---|---|
| **G-02** | 用户小文件自助上传闭环（授权→直传→固化→异步校验→READY） | ❌ 未实现（当前仅管理员 CLI 导入） | G-03 |
| **G-06** | 代导入正式化 + 输入授权 | 🟡 **List/Get 授权已完成**；代导入已由 `ImportCSV`+CLI 承担，剩余为命名正式化/大文件入口 | G-02、G-03 |
| **G-04** | 重新训练 `source_execution_id` | ❌ 未实现（仅需改动受理层 403 分支） | 无 |
| **G-05** | metrics 端点 + 日志有界归档 | ❌ 未实现 | 无 |
| **G-08** | 前端对接需求（后端侧保障） | ❌ 前端未接入 | G-02、G-04、G-05 |

### C. 加固（可与主链并行，不阻断流程）

| 缺口 | 内容 | 现状 |
|---|---|---|
| **G-13** | 受理暂停开关正式化（D16-C05 边界语义） | 🟡 CLI 已存在（`modeldev-pause/enable`），仅缺边界语义验收 |
| **G-10** | 受控清理通用化 | 🟡 CLI 已存在，覆盖范围受限 |
| **G-12** | 受保护资产禁删（D16-C29） | ❌ 未实现 |
| **G-07** | 产物 tar 打包 | 🟡 字段在（`create_tar_bundle`），未启用 |
| **G-09** | 多租户负向验证 | 🟡 待验收 |
| **G-11** | 离线记录检查工具入交付链 | ❌ 未实现 |

### 建议实施顺序（对齐本节分档）

```
阶段一（去硬编码，让"通用程序"成立）
  跨仓前置：C1 完成 镜像空间(EnsureImageSpace) + namespace pull secret 预置  ← 先决
  G-01 镜像登记合同 + 去硬编码        ┐ 并行
  G-03 输入/输出通用化（RAW_FILE）    ┘ 先固定 fixture 与合同测试

阶段二（用户自助闭环）
  G-02 小文件上传 → G-06 代导入正式化（复用既有 ImportCSV 链）

阶段三（体验完整）
  G-04 重训（低工作量）→ G-05 metrics/日志 → G-08 前端接入

阶段四（加固验收）
  G-07 / G-09 / G-10 / G-11 / G-12 / G-13
```

**与 D16 验收矩阵的对应重点**：C06/C07/C08（输入）、**C15（第二个已登记镜像——G-01 的验收标准；须含"镜像前置链路"：先 `EnsureImageSpace`+`IssuePublisherCredential` 再登记）**、C09/C10（幂等/冻结，已 ✅）、C32（无人工串流程）。

### 与 §2/§3 的差异说明

本节**不新增缺口编号**，仅对既有 G-xx 重排优先级并同步现状。A2/C4 状态以本节及 §2 为准（"已接通/已存在"）。凡本节与 §2/§3/§4 冲突，**以本节为准**。

---

## 9. 修订记录

| 日期 | 修订内容 |
|---|---|
| 2026-10-13 | 据评审补引 D04/D07/D11/D16/O15；新增 G-12、G-13。 |
| 2026-10-15 | 据源码逐项重核，更新落后于代码的现状事实（G-06 授权已接通等）；拍板 G-01 镜像路线 A2+T0；新增 §8 按"阻断主链"重排优先级；补 G-01 镜像前置链路与拉取凭据机制、明确 T0 调用 RPC（`GetImage`）、镜像校验限定为导入期、G-02 降至 B 档，并修正文件落点。 |

> 当前结论以正文为准，本表仅作留痕。
