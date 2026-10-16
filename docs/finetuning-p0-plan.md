# CPU 微调 P0（Model 接入）实施计划

编制日期：2026-10-16

**文档来源与信任等级**（读本文前先分清三类来源，不要混用）：

| 来源 | 文档 | 性质 |
| --- | --- | --- |
| **前一轮实现者交付** | [验收手册](finetuning-manual-test.md)、[后续工作交接](finetuning-next-handoff.md) | **权威上游**：M3-A 已完成部分的既有实现与交接说明，是本文的阶段前提 |
| **我方（接手方）agent 产出** | [实际调用表](finetuning-model-call-table.md)、[owner 问题函](finetuning-model-owner-questions.md)、[M3-A 集群复核](finetuning-m3a-cluster-review-20261016.md) | **待验证的内部初稿**：我方自己的分析与提问。**所有权在我方，不具外部权威性**，须经复核才可采信 |
| **唯一规范** | `docs/design/ani-finetuning-v4.2.2/spec.md` | 冲突时以 spec 为准。**2026-10-16 已裁决**：以仓内本份为唯一 anchor（Fedora 侧 `.scratch/finetuning-cpu/spec.md` 为上轮实现者机器文件，本方不可达，不作 anchor） |

说明：本文是**排期与责任分配**，不替代唯一 spec，不含任何 PASS 判定。本文 §2 即对上表第二类"内部初稿"做的复核。

---

## 0. 一句话现状

P0 卡的不是"Model 没部署"，也不是"ModelDev 缺代码"，而是**两个服务之间的调用根本不存在**：ModelDev 侧没有调用方代码和配置，Model 侧没有登记 ModelDev 身份。往外看，真正卡住的只有三类事——**Model 提供方拍板 5 件、Model 提供方实现 4 件、环境 owner 给容量**。剩下全部是我们本地就能写的。

---

## 1. 我方 agent 那两份文档是什么

这两份是**我方（接手方）** agent 于 2026-10-16 产出的 P0 分析初稿。**关键前提：所有权在我们，不在前一轮实现者**——所以它们不是"别人给的结论"，而是"我们自己的假设"，采信它们等同于采信自己的判断，举证责任在我方。两者都不是验收结论，未对 FT/MR/PA 下任何判定，也从未真实发起过 mTLS 调用。

它们与前一轮实现者交付的两个文档的关系是：**后者是前提，前者是对前提的延伸分析**——[后续工作交接](finetuning-next-handoff.md) §3 指出下一步是"真实自动登记链"，这两份初稿就是把那条链**拆成调用表和待拍板问题**的一次尝试。

| 文档 | 定位 | 回答什么 | 性质 |
| --- | --- | --- | --- |
| `finetuning-model-call-table.md` | **内部版**：P0 前置合同 + 缺口清单 | ModelDev→Model 要调哪些 RPC（C0–C7）、每条字段填什么、哪些已具备、哪些缺、待谁确认（T1–T13） | 静态结论来自本机源码；部署值来自集群只读读回；未改任何东西 |
| `finetuning-model-owner-questions.md` | **对外版**：给 Model owner 的拍板函 | 同一批事实中**只能由对方决定**的部分：Q1 身份 SAN、Q2 放行方法、Q3 principal 分支形状、Q4 超时预算、Q5 租户映射、Q6 API 模块版本 | 一封待发出的协商函，发出后等回复才能真实联调 |

**两者关系**：同一件事的内外两面。call-table 是"我方把合同和缺口钉死"，owner-questions 是"把其中只能对方拍板的 6 件事摘成一封能直接回复的信"。owner-questions §四明确写了：read 路径与 D09.3 仲裁**不阻塞对方回复**，但真联调必须等 Q1/Q2/Q6。

同一批还有我方 agent 的第 3 份产出：[M3-A 集群侧只读复核](finetuning-m3a-cluster-review-20261016.md)——这份复核的是**前一轮实现者**手册里的 M3-A 集群证据，结论是证据自洽、另列 3 处口径待对齐（见本文 §2.1 P6）。

### 1.1 call-table 指出的四个缺口（均已由 §2 复核）

| # | 缺口 | 位置 | 谁能解 |
| --- | --- | --- | --- |
| G1 | Model 未登记 ModelDev 调用身份 → 必被 `PermissionDenied` | Model 侧 | Model 提供方（阻塞） |
| G2 | ModelDev 侧**完全没配** Model 端点，调用方代码与配置都不存在 | ModelDev 侧 | 本地可写（但无 G1 无法验收） |
| G3 | 产物 `MERGED_HF_MODEL` 与 Model 的 `safetensors\|gguf\|pytorch` 枚举不匹配 | 双方 | Model 定映射 |
| G4 | D09.3 登记派发仲裁未实现，`RegisterModel` 仍只返回 `MODEL_DEPENDENCY_UNAVAILABLE` | ModelDev 侧 | 本地可写（可单测） |

补充结论 §4.6（源码已定）：Model 与 ModelDev 虽共用同一个 RustFS，但 bucket 不同，而 `bucketForTenant` 在 `tenantBuckets=false` 时恒返回单一 bucket，**"直接引用原 publication"在代码上不成立**，存储交接只能走 **C7 受控复制**。

---

## 2. 我方初稿的复核记录

对我方 agent 两份初稿做的抽样验证。只读本机源码，未连集群、未改任何文件。结论：**两份初稿的承重结论全部成立**——即上文"所有权在我方"的风险已实际核过一遍，不是自说自话。

| 复核项 | 证据 | 结果 |
| --- | --- | --- |
| `RegisterModel` 仍是桩 | `internal/biz/finetuning_registration.go:43-51` 仅 `RecordRegistrationUnavailable` + 返回 `ErrModelDependencyUnavailable` | ✅ 成立 |
| 生产装配确实没接 Model reader | `cmd/ani-modeldev-service/command.go:98` 用三参 `NewManagedAdmissionResolver(releases, inputs, facts)` → `resolver.models == nil` → FINETUNING 恒 `ErrFinetuningNotEnabled`（`finetuning_admission.go:60`） | ✅ 成立，且给出精确断点 |
| ModelDev 无 Model 客户端 | 全仓无 `model.v1` proto/client，仅有 modeldev/training proto | ✅ 成立 |
| Model 策略是硬编码双身份 | `cmd/ani-model-service/rpc_policy.go:99-131`，仅 `ani-inference.ani.internal` 与 `governanceClientIdentity` | ✅ 成立 |
| 两处都要加才生效 | `ValidatedPolicy.expectedIdentities`（同文件 `:270-275`）是独立白名单 | ✅ 成立 |
| 非 Gov/Inference 拿不到 Principal | `ModelPrincipalMiddleware`（`:151-160`）第三分支直接 `next`；且 `Principal.Valid()` 要求 TenantID/Actor/Workload 三者非空（`internal/identity/context.go:27`） | ✅ 成立 |
| format 只有三个值 | DB CHECK `migrations/000001:25` + `internal/biz/model/model.go:45,67` | ✅ 成立 |
| 单 bucket 硬约束 | `internal/data/storage/minio.go:229-237` | ✅ 成立 |
| 查回：点查只返回 ready、列表能看到 pending | `queries/model.sql` `GetReadyModelVersionByExternalRef` 带 `v.status='ready'`；`ListModelVersions` 只过滤 `status <> 'deleted'` 且返回 `status` | ✅ 成立 |
| 本地 config 无 model 端点 | `configs/config.yaml` 仅 server 段 | ✅ 成立（**但部署配置来源未核，见 §3.3**） |
| 部署版本 `a21e0a7` 真实存在 | `git log a21e0a7` = `fix(auth): align Governance caller with exact DNS identity`，其 `rpc_policy.go:19` = `"ani-governance"` | ✅ 成立 |

### 2.1 复核中发现的、两份文档没讲清的问题

| # | 问题 | 影响 |
| --- | --- | --- |
| P1 | ~~spec 版本歧义~~ **已裁决（2026-10-16）**：以仓内 `docs/design/ani-finetuning-v4.2.2/spec.md` 为唯一 anchor；Fedora 侧 `.scratch` 版为上轮实现者机器文件，本方不可达，不作 anchor | 已记录于文首"文档来源与信任等级" |
| P2 | **参考基线易踩错**：本地 `ani-model-service` master `4b9b3b5` 里治理身份是 `ani-governance.ani.internal`，而部署版 `a21e0a7` 已是 `ani-governance` | 拿 master 当参照会得出错误结论；必须以 `a21e0a7`（后继 `437822a`）为准 |
| P3 | **新文档全部未提交**：`docs/finetuning-model-call-table.md`、`-owner-questions.md`、`-m3a-cluster-review-20261016.md` 及整个 `docs/design/ani-finetuning-v4.2.2/` 均为 `??` 未跟踪 | 换机即丢；应先提交 |
| P4 | **proto 消费方式存在新阻塞**：Gov《服务间调用规则》§5 明确"只消费接收方公布的固定版本 API 模块，**不复制 Proto、不用本地 replace**"。ModelDev 当前 go.mod 无 Model 模块依赖 | 需要 Model 侧发布可消费的模块版本——**已补入 call-table §2 前置项 C0 与 §5 T13、owner-questions Q6**，见 B2 |
| P5 | call-table §5 的 T 编号缺 T4/T9（T1/T2/T3/T5…） | 只是编号断层，不是漏项；已在 call-table 文末注明为保留号 |
| P6 | M3-A 手册 3 处口径差异（driver 镜像 digest、A/B 实际 source-commit 为 `f7b852e` 而非手册立的 `1ca8f03`、kubeconfig 入口） | 建议修订手册表述，不追溯改判历史结论 |

---

## 3. 后面要做什么

原则：**能本地做的先做，不要干等 Model owner**。call-table §6 的顺序是对的，但漏了"等回复期间本地可以并行推进 1.1–1.6"这一层。

### Phase 0 · 立刻可做，零外部依赖（今天就推）

| # | 动作 | 产出 | 责任 |
| --- | --- | --- | --- |
| 0.1 | 发出 `finetuning-model-owner-questions.md`，附 call-table §3/§5 | Q1–Q6 回填 | 微调对接方 |
| 0.2 | ~~裁决 spec anchor（P1）~~ **已完成（2026-10-16）**：以仓内 `docs/design/ani-finetuning-v4.2.2/spec.md` 为唯一 anchor | 文档头一句结论 | 内部 |
| 0.3 | 对齐 M3-A 手册 3 处口径（P6）：改表述，不改历史结论 | 手册修订 | 内部 |
| 0.4 | 把"Model 参考基线 = `a21e0a7`/`437822a`，非 master"写进文档头（P2） | 防踩坑 | 内部 |
| 0.5 | 提交上述未跟踪文档（P3） | Git 记录 | 内部 |

**出口**：一份回填后的合同表 + 一致的手册口径。0.2–0.5 全部可与 0.1 并行。

### Phase 1 · 本地可写、可单测（等回复期间就能开工）

关键判断：**这些改动没有 Q1/Q2 也能开发并单测，只是不能标 PASS**。不要把它们当"等对方回复后的活"。

| # | 动作 | 对应 | 说明 |
| --- | --- | --- | --- |
| 1.1 | 加 Model 端点 + mTLS 客户端证书配置位 | T11 / G2 | **先查清集群上 `ani-modeldev` 的 config.yaml 实际来源**（helm values？），只改仓库 `configs/config.yaml` 大概率不生效 |
| 1.2 | 定 Model proto 消费方式 | B2 / P4 | 按 Gov §5 只能消费已发布模块，不能用 replace；若 Model 暂不可发布，先用**窄接口 + 本地替身**做消费者合同测试，但不得算 MR 通过 |
| 1.3 | 实现 `FinetuningModelFactsReader` 真实实现（C1/C2 只读）并接线 | G2 | 断点已定位：`command.go:98` 需换成四参 `NewFinetuningAdmissionResolver`（或让 `NewManagedAdmissionResolver` 带 models） |
| 1.4 | 实现 D09.3 派发仲裁 | G4 / FT25.1–3 | 与 Stop/关闭/deadline **共用执行行锁**；事务内重核 spec/阶段/业务键/目标/停止状态/剩余预算，**事务外才做 RPC 与对象 I/O**；能区分"意图已存 / 已获准派发 / 结果未明或已确认"；new 分支 `CreateModel` 与 `CreateModelVersion` **分别仲裁** |
| 1.5 | C3–C7 客户端 + 幂等键 | MR03–07 | 业务键固定 `md-container:<execution_id>` / `md-<execution_id>` / `exec-<execution_id>`；**先查证再有限重放**；404 不单独证明"原写未发生"；不得把 versioned object 降为 latest |
| 1.6 | 域内单测 | FT25.1–3 域内部分 | 同键同内容重放、同键异内容拒绝、Stop 先于派发→该外部写次数为 0、派发先于 Stop→容器收据保留且新版本写为 0 |

**出口口径**：只能报 `CODE` / `ANI01_PASS`（验证宿主为远端 ani-01，见 B11；Fedora 为上轮实现者机器，本方不可达）。MR01–MR08 与相关 FT 的 Model 断言保持 `NOT_RUN`——替身的权限/事务结果不算 MR 通过（spec D08 §8.1）。

> **验证宿主变更（2026-10-16 实测）**：远端 ani-01（ttyd，经 `local-remote/ttyd-exec.py`，多网卡机器须加 `--source-ip 192.168.203.132` 否则超时）可达；但 go 1.22.2 低于 repo 要求的 1.26.9，psql/docker 不在 PATH，环境正在加装。就绪后须在其上重建 `make verify` 与真实 PG 测试流程，旧 Fedora 的门禁结论不自动迁移。

### Phase 2 · 需要 Model 提供方（Q1–Q6 回填后）

| 谁 | 做什么 |
| --- | --- |
| Model | ① 签发并登记 ModelDev 的唯一 DNS SAN；② `policy map` + `expectedIdentities` **两处都加**；③ **新增 principal 构造分支**（回答 Q3：actor 前缀、workload 取值）；④ 定 `MERGED_HF_MODEL` 的 format 映射；⑤ 定"目标可写检查"承载方式；⑥ 放行 `ListModelVersions` 或补按业务键的点查（T12）；⑦ 确认 bucket/配额约束；⑧ 发布可消费的 API 模块版本（Q6/T13） |
| 环境 owner | 新客户端证书进 secret、ModelDev 部署注入、放行方法清单 |
| 双方 | 单次 RPC 超时与重试预算（Q4）；Model tenant UUID 与 Gov 租户映射（Q5，T10） |

**出口**：打通一条真实最小链路——`调用方业务 → mTLS → 接收方权限/持久化 → 可观察结果`（Gov《服务间调用规则》§5 原文要求）。

### Phase 3 · M1-B 真实联调（硬前置：集群容量）

> **硬前置**：目标集群 **PVC 19/19、380Gi 用满**。新 case 需要一个新 20Gi PVC。**不拿到环境 owner 确认的容量与本次有限配额，一个 case 都建不了。** 这条不在 Model 手上，别等错人。

1. 新 case 走官方 renderer 生成新 execution/op/新 case（不重放历史 case）。
2. 真实训练 → export/verify → collect/publish（M3-A 已验证的子链，不需要重做）。
3. **真实 `CreateModelVersion`**，存储走 **C7 受控复制**（源码已定 A 不可行）。
4. 读回核原字段 + `READY`，核 tar 的 version/size/SHA 与原 publication 一致。
5. 正常关闭；原业务键重放只返回同一 ModelVersion。

**出口**：MR01–MR08 逐项实际断言；FT07/08/10/18–21/25/28 的 Model 相关部分。

### Phase 4 · 之后（现在不要做）

| 优先级 | 内容 | 前置 |
| --- | --- | --- |
| P1 | Gov 真实来件/精确 Release/服务身份/持久投递 | 可与 P0 并行核对合同 |
| P2 | BFF 真实表单/查询/停止/登记恢复 | 依赖 P1 合同；不得用测试来件或静态页面抵扣 |
| P3 | FT29/C23：用**本次新登记**的 ModelVersion 走既有 Inference/KServe | 依赖真实 ModelVersion |
| P4 | 整版 SCHEME_FINAL + 正式启用 | PUBLIC `FINETUNING`/`current` 指针只能由 owner 按批准流程切换 |

---

## 4. 阻塞清单（谁卡谁）

| # | 阻塞 | 卡住什么 | 责任方 | 现状 |
| --- | --- | --- | --- | --- |
| B1 | Model 未登记 ModelDev 身份 | 一切真实 mTLS 调用 | **Model** | 阻塞，已问 Q1/Q3 |
| B2 | Model 未发布可消费的 proto 模块 | 1.2 客户端依赖 | **Model** | 新发现，需单列 |
| B3 | format 枚举不匹配 | C4 登记 | **Model** | 阻塞，已问 Q2/T6 |
| B4 | "目标可写检查"不存在 | MR03 | **Model** | 阻塞，已问 T7 |
| B5 | `ListModelVersions` 未放行给 ModelDev（点查只答 READY/无） | MR05 查证 | Model | 阻塞，已问 T12 |
| B6 | Model tenant UUID ↔ Gov 租户映射 | 每次调用 | Gov + Model | 阻塞，已问 Q5 |
| B7 | **PVC 19/19 用满** | Phase 3 全部 | **环境 owner** | 阻塞，**与 Model 无关，需单独催** |
| B8 | Fedora 为上轮实现者机器，**本方不可达**，`.scratch`/`.private`/镜像台账等历史材料无法取得 | M3-A 历史集群证据的复核升级为全量 | 已搁置（用户确认） | 历史证据保持原记录，不阻塞新开发 |
| B9 | spec anchor 歧义（P1） | 文档一致性 | 内部 | **已裁决（2026-10-16）**：以仓内 `docs/design/ani-finetuning-v4.2.2/spec.md` 为唯一 anchor |
| B10 | 部署 config.yaml 的真实来源未核 | 1.1 改动能否生效 | ModelDev | **已核（2026-10-16）**：部署实际来自 `ani-system/md-fixed-config`（`-conf /etc/modeldev/config.yaml`），改仓库 `configs/config.yaml` **不生效** |
| B11 | ~~ani-01 验证宿主未就绪~~ | `make verify`、真实 PG 测试 | 我方 | **已解除（2026-10-16）**：宿主已建成，完整 `make verify` exit=0，见 §7 |
| B12 | **集群正在装 GPU 卡，环境波动**（节点 15:12 重启；`ani-model`/`ani-inference`/`ani-resource` 一度 CrashLoopBackOff，Model 失败于 RustFS/PG 超时） | Phase 3 全部真实联调 | 环境 owner | 阻塞（用户 2026-10-16 告知）；**非代码问题**，须等环境稳定后重测 |
| B13 | **ModelDev 的 k8s/protobuf 依赖被 pin 被动抬升**：引入 Model 模块后 `k8s.io/*` 0.34.1→0.36.0、`protobuf` → 伪版本、`json-patch.v4` → v4.13.0 | 1.2 的依赖影响面 | 双方 | **新发现**，见 §7.2；`make verify` 已通过，但升级幅度需确认 |
| B14 | **`BaseModelRef.Files`/`Object` 无对应 Model RPC**：Model 只返回单个 `storage_path` + 整包 `size/SHA`，**不返回逐文件清单** | MR02 基模读取、C1 | Model | **新发现**，见 §7.3；这是比 format 更靠前的真实缺口 |
| B15 | ~~D09.3 登记派发仲裁未实现~~ | 本地实现（G4） | ModelDev | **已解除（2026-10-16）**：领域类型 + 持久化 + 6 例真实 PG 单测，`make verify` exit=0，见 §7.5 |
| B16 | ~~登记 RPC 编排（C3–C5 调用编排）未实现~~ | 本地实现（G4 的编排部分） | ModelDev | **已解除（2026-10-16）**：biz 层 orchestrator + 窄端口 + 8 例单测，`make verify` exit=0，见 §7.6。**真实 `modelclient` 接线仍等 G1/G3/§4.6** |

---

## 5. 红线（不要做）

- **不冒用 `ani-governance` 身份**，也不绕开 Model 授权校验（spec A04/D08.1）。
- **不复制 Model 的 Proto、不用本地 `replace` 冒充互操作**（Gov §5 明文）。
- 不重跑历史用例、不借旧 case 改键、不续 deadline、不重置预算。
- 不删除/不扩额历史 PVC，不改 SQL 状态，不强拆 finalizer——19 PVC 用满**不构成**删除理由。
- 不把 `CODE`/`FEDORA_PASS`/替身结果升级为 MR PASS；`Ready`、`Job Complete`、`Workflow Failed`、`404`、`NotFound` 均不能单独判定业务通过。
- 不把"PUBLISHED + 依赖错误 + CLOSED"写成 A10 六项齐备的成功。
- 不顺手改共享 RustFS/CNI/MWC/VAP 或共享 Model/Gov/Inference 部署；那些由对应 owner 协调。

---

## 6. 下一步（若只做一件事）

**发出 owner-questions 并同时启动 Phase 1.3 / 1.4**：前者是唯一零依赖的外部动作，后者是零依赖的本地实现，且正是"无 Model 也能测"的核心（D09.3 仲裁 + FINETUNING 接线）。两者互不等待。

---

## 7. 2026-10-16 实施记录（已实际执行并验证）

本节记录**已在本轮真实执行**的动作与结果，与 §3 的计划项区分开。所有编译/测试都在 ani-01 宿主机（验证宿主）上跑，**未部署、未发起任何真实 Model 调用**。

### 7.1 验证宿主已建成，`make verify` 全通过（解除 B11）

- **工具链**：ani-01 自带 go1.22.2（低于 repo 要求 1.26.9）。用 `GOTOOLCHAIN=auto` + `GOPROXY=https://goproxy.cn,direct` **实测可拉到 go1.26.9**（`proxy.golang.org` 不可达，必须走 goproxy.cn）。
- **PostgreSQL**：`scripts/with-test-postgres.py` 依赖 Docker，而 ani-01 **无 docker/podman/nerdctl**。改为 `apt` 安装 postgresql-16，建**独立集群** `modeldevp0`（`127.0.0.1:55432`，systemd 托管，避免 logind 回收共享内存段），并建双角色：
  - `cpu_p01_owner`（管理员，跑迁移）
  - `cpu_p01_runtime`（`NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS`，仅回环）
  与仓库包装器同一契约（同库、分离角色、runtime 无特权），因此 DB 门禁测试可用真实 PostgreSQL 运行。
- **结果**：`go test ./...` **exit=0、0 失败**；完整 `make verify` **exit=0**（含 `verify-source` 生成一致性、`go vet`、`go build -trimpath ./...`、`go mod verify`、revision-upgrade 真实迁移用例）。
- **宿主位置**：`/home/ubuntu/ani-modeldev-p0-20261016/`（`source/` 为 `origin/main@ee46246` 干净 checkout，`bin/testpg.sh` 为隔离库管理脚本，`testpg-secrets/env.sh` 仅本机 600 权限）。**未改动集群、未改共享资源。**

### 7.2 已落地代码（可编译、可过门禁）

| 项 | 内容 | 验证 |
| --- | --- | --- |
| `internal/conf/v1/conf.proto` | 新增 `ModelConnection`（endpoint/ca_file/client_dns_name/certificate_file/private_key_file/server_name/actor/timeout），`GovernanceCommand.model = 7`；**缺省即禁用 FINETUNING** | `make config` 重新生成 `conf.pb.go`，buf lint/build/generate 全过 |
| `internal/conf/v1/validate.go` | 新增 `validateModelConnection`：材料必须绝对路径、endpoint 必须字面 `host:port`、client 与 server DNS 身份必须**精确且互异**、actor 有界、超时 1ns..30s；**部分配置一律拒绝**（不能静默降级） | 新增 `model_test.go`：19 例负向 + 缺省有效 |
| `internal/data/modelclient/` | ModelDev→Model 业务 mTLS 适配器：TLS1.3 强制、客户端证书必须**恰好一个指定 DNS SAN 且无通配/URI/IP/email SAN**、服务端精确 DNS 校验、每次调用**重建** `x-ani-tenant-id`/`x-ani-actor`/`x-ani-request-id`、tenant 必须规范 UUID；封装 7 个 RPC | `go build` / `go vet` / `gofmt` 全干净 |

### 7.3 本轮新发现的三个重要事实

**(a) 部署配置来源已定（B10）**：`ani-system/ani-modeldev` 用 `-conf /etc/modeldev/config.yaml` 启动，该文件来自 ConfigMap **`md-fixed-config`**（另有 `modeldev-runtime-private` 挂载 Secret）。即"改仓库 `configs/config.yaml`"**不生效**，必须改 ConfigMap。该 CM 当前**完全没有 model 段**，与 call-table G2 一致。

**(b) 消费基线比预期宽松，但代价是把 k8s 依赖整体抬高（B13）**：Gov pin 的 `github.com/liangzai006/ani-model-service v0.0.0-20261013092523-8951c85bc150` **实测可下载**，且已含全部 7 个目标 RPC 的客户端桩，**proto 相对部署版只多一个 `SearchExternalModels`**（微调不用）。但该模块的 `go.mod` 直接要求 `k8s.io/* v0.36.0`、`protobuf` 伪版本，导致 ModelDev 的 `k8s.io/*` 被从 **0.34.1 抬到 0.36.0**（另有 `json-patch.v4`→v4.13.0）。
- **本仓现状**：单一根模块，因此该升级**必然波及全仓**。`make verify` 已通过，说明当前代码兼容；但这是一次**未经评审的依赖抬升**，应由双方确认接受，或改用下面的 (c)。
- **替代方案（有仓内先例）**：`ani-resource-service` 与本仓 `ani-governance` 都把"给外部消费的 API"拆成**嵌套子模块**（`ani-resource-service/api/instance/terminal/go.mod`、`ani-governance/api/quota/go.mod`），Gov 正是以子模块路径消费 Resource。**若 Model 也把 `api/model/v1` 拆成嵌套模块，消费方只拿 protobuf，不会被动升级 k8s/PG/minio 依赖栈。** 这是比"pin 整个服务模块"更干净的做法，建议作为 Q6/T13 的推荐答案。

**(c) `BaseModelRef` 需要的逐文件清单，Model 现有 RPC 给不出（B14，比 format 更靠前）**：
- ModelDev 侧 [BaseModelRef](../contract/cpup01/finetuning_snapshot.go) 要求 `Files []BaseModelFile`（逐文件 path/size/SHA）、`Object FixedObjectRef`（含 **`VersionID` 固定版本**）、`Scope`，prepare 阶段会按此**逐文件核验并解 tar**（[finetuning_prepare.go:336-383](../internal/data/workspace/finetuning_prepare.go)）、并按 `Object.VersionID` 做固定版本读取。
- Model 的 `GetModelVersion` 只返回**单个** `storage_path` + 整包 `size_bytes`/`checksum_sha256`，**没有任何逐文件清单字段**（`ModelVersion` 无 files 字段；`ModelArtifact` 只有整包 provider/reference/sha256）。
- 结论：**"基模读取（MR02/C1）"缺的不是权限，是数据形状**——Model 需要补一个能表达"固定版本 + 逐文件清单"的窄接口，或明确 ModelDev 改为只按整包 tar 读取（那要改本仓 prepare 的核验契约）。**这条应加入 Q1–Q6 一并拍板**，且优先级高于 format 映射。

### 7.4 环境侧注意（B12，非代码）

用户告知集群**正在装 GPU 卡**。实测同期现象：节点 `15:12` 重启（`uptime` 20 分钟）、`ani-model-service`/`ani-inference-service`/`ani-resource-service` 一度全体 `CrashLoopBackOff`（Model 报 `check MinIO Storage … context deadline exceeded`，随后 PG 超时恢复）、`ani-platform/postgresql`/`rustfs`/`milvus` 均在重启，另有 `ani-three-service-deploy-20261016` 在同时进行。
**判断：这些是环境波动，不是代码缺陷。** 因此 §3 Phase 3（真实验收）**整体推后**至环境稳定；Phase 1/2 的本地实现不受影响。**开工前先确认环境稳定**，避免把环境噪声误判为代码失败。

### 7.5 D09.3 登记派发仲裁已实现并验证（B4/B5 完成）

本节是 §3 Phase 1.4 + 1.6 的实际落地。**全部在 ani-01 验证宿主上用真实 PostgreSQL 跑通，未部署、未发起任何真实 Model 调用。**

**领域类型**（`internal/biz/finetuning_dispatch.go`）：

- `FinetuningRegistrationOperation`：`CREATE_MODEL` / `CREATE_MODEL_VERSION` / `CONTROLLED_COPY`——new 分支容器与版本写**分别仲裁**，受控复制是独立操作。
- `FinetuningDispatchState`：`IN_FLIGHT` / `CONFIRMED` / `UNKNOWN`。**`UNKNOWN` 不等同失败**：已获准而丢响应的请求仍可能远端成功，只有按原键查证才能定论；`NotFound` 不单独证明"没写过"。
- `FinetuningRegistrationDispatch`：每项操作的在途事实，含 `RequestID`（调用追踪 ID）、`ContentSHA256`（绑定冻结内容，防同键重放改内容）、`ReceiptID`、`FailureCode`。
- `FinetuningRegistrationDispatchRepository`：`PermitRegistrationDispatch`（取得本次派发资格）/ `RecordRegistrationOutcome`（保存真实观测结果）。
- 校验：`ValidRegistrationDispatch` 要求许可与观测时间单调、`RequestID` 有界、**截止点必须等于原登记意图的截止点**——即预算只在意图落库时领取一次，任何后续派发**不得重领**（对齐 spec D10「不每次重新领取 600 秒」）。

**持久化实现**（`internal/data/lifecycle/finetuning_dispatch.go`）：

- 全部走既有 `repository.mutate(...)`，即**与 Stop/关闭/deadline 共用同一执行行锁** `LockRuntimeIdentity`；事务内重核 spec/阶段/业务键/内容/原目标/停止状态/剩余预算，**事务外才做 RPC 与对象 I/O**（`PermitRegistrationDispatch` 只落事实，不发网络请求）。
- `RecordRegistrationIntent`：首次外部写之前固化原内容，并一次性固化登记预算。
- `PermitRegistrationDispatch`：同一操作重复调用**只返回已存的在途事实、不再重发**（`SendPermit=false`）；new 分支下版本写必须已有**已确认的容器收据**，`IN_FLIGHT` 的容器不构成授权。
- `RecordRegistrationOutcome`：**不需要执行仍处于 open**——关闭栅栏后到达的迟到响应仍是真实事实，必须如实保留；`CONFIRMED` 唯凭原目标/版本/对象的核验后才允许；仅当版本写 `CONFIRMED` 时投影状态才变 `READY`。
- `validRegistrationFacts` 已同步新字段，并新增 `validRegistrationDispatches`：单操作去重、内容绑定一致、共享意图截止点、new 分支的容器先于版本。

**域内单测**（`internal/data/lifecycle/finetuning_dispatch_test.go`，6 例，真实 PG）：

| 断言 | 覆盖 spec |
| --- | --- |
| 意图在首次写之前固化内容与预算，重放不改内容/预算 | 9.3.1 / D10 预算一次性 |
| 同操作重放不重发，`RequestID` 保持原值，不重领预算 | 9.3.3 |
| **Stop 先于派发 → 该外部写次数为 0**（拒绝则不落任何 dispatch） | FT25.1 |
| **派发先于 Stop → 容器收据保留、版本写为 0**、不自动换目标、仅容器写不判 READY | FT25.2 / 9.3.2 |
| 版本写须有已确认容器；`UNKNOWN` 不判 READY；`CONFIRMED` 才 READY；READY 后不产生新写 | 9.3.1 / 9.3.2 |
| 结果必须匹配原许可（换 RequestID / 无收据确认 / 未获准操作均拒） | 9.3.3 |

**验证结果**：`gofmt` 干净、`go vet` 干净；`go test ./internal/data/lifecycle/` 整包 **exit=0**；完整 `make verify` **exit=0**（含生成一致性、vet、build -trimpath、mod verify、revision-upgrade 真实迁移用例）。**不改变 M3-A 既有 M06/M07 语义。**

**边界（诚实标注）**：本轮只交付**本域仲裁**。对外可观测的"真实 mTLS 调用次数为 0"仍需 §4.1 提供方身份后才能端到端证明；本单测以"未落 dispatch / 无 permit"作为该外部写在**本域**的等价证明，不宣称 MR 通过。

### 7.6 登记 RPC 编排已实现（B16，C3–C5 的调用编排）

本节是 §3 Phase 1.5 的**编排部分**落地：把"具体外部写"从"只仲裁"推进到"仲裁 + 真实调用 + 原结果查证"。

**落点**（`internal/biz/finetuning_registration_write.go`，按用户选定"biz 层新增 orchestrator"）：

- `FinetuningModelRegistry`：**窄出站端口**，只有 4 个方法——`CreateContainer` / `RegisterVersion` / `FindContainer` / `FindVersion`。实现只管传输、调用方身份与请求元数据，**不决定是否获准派发、不自行重试**。
- `FinetuningRegistrationOrchestrator`：依赖 `FinetuningRegistrationDispatchRepository`（§7.5）+ 上述端口；**biz 层编排，data 层只持久化，modelclient 只传输**，与既有分层一致。
- `EnsureContainer` / `EnsureVersion`：先仲裁（落 dispatch 事实），**事务外**才发 RPC；同一个 key 的重放只回读原事实、不重发。

**关键不变量（都写成校验，不靠约定）**：

| 不变量 | 位置 |
| --- | --- |
| 容器写**只属于 new 分支**（有 `TargetModelID` 时拒绝） | `validContainerWrite` |
| 版本写的 `ModelID` 必须是**冻结的原目标** | `validVersionWrite` |
| 版本写的 `Version` 必须是**冻结的 `exec-<exec>`**，`format` 必须非空 | `validVersionWrite` |
| 版本写的 `ChecksumSHA256`/`SizeBytes` 必须等于**冻结的 bundle 对象** | `validVersionWrite` |
| `CONFIRMED` 唯凭**精确读回**：target/tag/object/size/format/`status=ready` 全等 | `matchesFrozenVersion` |
| 丢响应 → `UNKNOWN`（**不是失败**），后续只查证、不重发 | `settle` + `EnsureContainer` 的 `!SendPermit` 分支 |
| 停止/预算已到 → 派发被拒，**provider 调用次数为 0** | `PermitRegistrationDispatch` 返回 `ErrPipelineDispatchBlocked` 时直接上抛 |

**注**：`format` 映射（G3/T6）与"目标可写检查"（T7）**不由本层猜**——`FinetuningVersionWrite.Format` 是显式输入，由调用方（提供方拍板后）填入；本层只校验"非空且与读回一致"。

**域内单测**（`internal/biz/finetuning_registration_write_test.go`，8 例，纯 biz，带可观测调用计数）：

| 断言 | 覆盖 |
| --- | --- |
| 容器写一次并确认；重放**不再写**（creates 保持 1） | 9.3.3 重放 |
| 丢响应 → `UNKNOWN` + `MODEL_WRITE_UNRESOLVED`；再次进入只查证不重发 | 9.3.3 / 9.3.2「不换 key」 |
| 迟到成功**只能被查证确认**，绝不重发 | 9.3.2「CreateModel 已完成或迟到成功」 |
| 空/非法版本写被拒；正常写确认 | 输入校验 |
| 改 SHA / 改 size / 改 tag → 全被拒，**provider 调用 0** | 冻结内容绑定；「同 key 异内容拒绝」 |
| existing 分支不写容器；换目标被拒；原目标通过 | D08.3 原目标 |
| 版本 `pending` → `UNKNOWN`，**不投影 READY** | 9.3.1 / 「HTTP 成功 ≠ READY」 |
| 停止/预算已到 → 派发被拒，**provider 调用 0** | FT25.1 |

**边界（诚实标注）**：本层用可观测替身证明"**本域发起的外部调用次数**"，但**替身的权限/事务结果不算 MR 通过**（spec D08 §8.1）。真实 `modelclient` 接线与真实调用仍等 §4.1（Model 身份）+ G3（format 映射）+ §4.6（存储交接）。**本层不含** `FindVersion` 的 `ListModelVersions` 兜底（C5 的 pending 观测），待 T12 放行后补。

---

## 声明

- 本文只做排期与责任划分，**不含 PASS/BLOCKED 判定**，不替代唯一 spec。
- 本文引用的"实际调用表 / owner 问题函 / M3-A 集群复核"三份文档是**我方 agent 的内部初稿**，所有权在本方，**不具外部权威性**；其结论在未复核前不得对外当作已定合同或已证事实引用。前三类来源的划分见文首"文档来源与信任等级"。
- §2 的复核结论来自本机源码只读阅读，已标注具体文件与行号；**未连集群、未部署、未发起调用**。其中标注为"集群只读读回"的部署侧事实（如 Model 部署版本 `a21e0a7`、集群 `ani-modeldev` 配置内容）来自我方初稿，**本机未复验**。
- 标 `待查/未核` 的项未经确认，不得当作已定合同引用。
