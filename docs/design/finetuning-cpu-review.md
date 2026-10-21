# 《模型微调（受管微调 / LoRA SFT）能力设计》技术复核报告

- 复核对象：`ani-modeldev-service/docs/design/finetuning-cpu.md`（重命名前为 `cpu-p01-finetuning.md`）
- 参考基线：`kubeflow文档/模型研发平台-v0.4/`（架构册 A01–A15 / 实施册 D01–D19）
- 复核方式：文档主张 ↔ 本仓代码事实 ↔ v0.4 基线三方交叉核对（含跨仓 `ani-model-service`、`ani-governance` 契约）
- 问题等级：**严重**（阻断落地 / 会导致返工或数据错误）、**一般**（需澄清或补齐，否则实施中会卡壳）、**建议优化**（可维护性 / 表达精度）

---

## 0. 总体结论

**文档整体质量较高**：复用边界判断清晰，差异化扩展点（基模加载、LoRA、工作区布局、tar 入包、model 登记）都有针对性说明，且大量偏差被文档**主动识别并列入"需修改文件"清单**（如受管步骤白名单、`in_tar` 新增字段、`output_verifier` 三重冲突、工作区子路径重叠校验）。

但存在**若干文档未识别或描述不准的真实阻碍点**，其中最关键的一类是：

> **`collector.go` 与 `pipeline_io.go` 的产物读写路径只支持单层文件名，不支持带 `/` 的子路径**，而文档 §4.3 契约中几乎全部产物都写成多层路径（`export/merged/...`、`training/adapter/...`、`reports/...`）——这会直接导致**收集与发布阶段全链路失败**，且文档 §4.2 未把这两处列入"需修改"，属于**被低估的实现阻碍**。

下文按用户要求的五个维度逐条展开。

---

## 1. 维度一：设计文档完整性

### 1.1 已覆盖的核心模块（✔）

| 模块          | 位置                                | 评价                                                              |
| ----------- | --------------------------------- | --------------------------------------------------------------- |
| 需求背景 / 现状梳理 | §1.1 / §1.2                       | 核心模块表 + 可直接复用能力表，颗粒度合适                                          |
| 架构与复用边界     | §1.2 / §5 / §7.1                  | 明确"复用骨架、扩展 capability"                                          |
| 流程说明        | §7.3 两阶段、§7.5 独立加载校验与模型登记         | 有 prepare→train→collect→export→verify→publish→register→close 全链 |
| 参数配置        | §2.2 / §4.3 / `finetune_cpu.yaml` | 学习率、冻结层、LoRA rank/alpha、cutoff_len 均有说明                         |
| 工作区布局与路径映射  | §2.5                              | `{ws}/training`、`{ws}/export` 与容器内 `/ani/...` 的映射明确             |
| 数据契约        | §7.2                              | `finetune_data.json` + `dataset_info.json` 落盘约定                 |
| 异常处理        | §6 验证边界、§7.5 加载校验失败分支             | **偏弱**，见 1.2                                                    |
| 验收标准        | §4.5 验证测试用例表                      | 有，但缺少可执行断言与失败判据，见 1.2                                           |

### 1.2 完整性问题

**【一般】1.2.1 异常处理章节不成体系**  
文档的"异常路径"散落在 §6（验证边界）、§7.5（模型加载校验）、§7.4（发布失败不删 PVC）。缺少集中章节覆盖：基模版本被删除/未 ready、下载 URL 过期（5 min TTL）重取、export 阶段 OOM、verify 阶段校验不通过、register 幂等冲突、tar 打包内容与 `RequiredFiles` 不一致等**失败后的状态机迁移与幂等重试语义**。  
→ **改进方向**：新增"§ 异常与失败路径"章节，逐场景给出 `失败阶段 → 执行状态 → 是否可重试 → 幂等键 → 清理动作` 表；与 D05 幂等键、D06 提交 worker 语义对齐。

**【一般】1.2.2 验收标准缺少量化断言**  
§4.5 用例表是"测什么"的清单，但没有"通过判据"。v0.4 D16 验收矩阵（C01–C34）是带编号与判据的，微调文档未给出对应的 C 编号子集。  
→ **改进方向**：为微调新增 `C-FT01…C-FTn`，每条含前置条件、操作、期望观测（日志关键字 / 输出对象 key 清单 / status）。

**【建议优化】1.2.3 未给出兼容性/回归声明**  
文档多处写"CPU 分支不变"，但未明确"如何验证 CPU 分支不变"（例如同一份 CPU 请求在改动前后 `spec_hash` 是否字节一致）。  
→ **改进方向**：增加一条回归断言——CPU 请求的 `Canonical()` 字节与 `spec_hash` 在改造前后必须完全一致。

---

## 2. 维度二：链路复用合理性

### 2.1 复用判断总体成立

受理 → 冻结快照 → KFP Run → 受管步骤 → Trainer → 收集 → 发布 → 关闭这条 CPU-P01 主干，微调确实可复用。文档 §1.2 的"可直接复用"表基本准确，且 §5/§7.3 已指出必须扩展的三处（受管步骤白名单、Trainer 两阶段提交、工作区/收集打包）。

### 2.2 复用问题

**【严重】2.2.1 收集/发布层不支持带 `/` 的子路径 —— 文档未识别（关键阻碍）**

- `internal/data/workspace/collector.go` `collectFile`：`syscall.Openat(root, name, ...)`，`name` 直接取 `required.RelativePath`，**Openat 的第二参不做多级解析**，传入 `export/merged/config.json` 会直接 `ENOENT`。
- `internal/data/workspace/pipeline_io.go` `capturePipelineFile`（L283）：显式 `strings.ContainsAny(expected.RelativePath, "/\\")` → 直接 `ErrInvalidWorkspaceOutput`。
- 而文档 §4.3 契约里 **12 条 `RequiredFiles` 全部是多层路径**（`export/merged/model.safetensors`、`training/adapter/adapter_model.safetensors`、`reports/verify.json` …），§7.4 还要求 tar 成员"剥离 `export/merged/` 前缀"——**前提是先把 `export/merged/xxx` 成功读出来**，当前代码做不到。
- 文档 §4.2 修改清单只列了"`collector.go` 微调按 `in_tar` 收集（仍逐项收集）"，**没有指出 `collectFile` 需要支持多级相对路径（逐级 `Openat` + `O_NOFOLLOW` 或等价安全打开）**，也没列出 `capturePipelineFile` 需要放开 `/`。  
  → **改进方向**：§4.2 显式补充：`collectFile` / `capturePipelineFile` 需支持多级相对路径，且**必须保持目录穿越防护语义**（逐级打开目录 fd 再 `Openat`，或对每级 `O_NOFOLLOW`），并在 §4.5 增"多级相对路径收集"负向用例（`../`、绝对路径、符号链接替换均须拒绝）。

**【严重】2.2.2 `PrepareInput` 硬编码 `data.csv` —— 文档未识别**

- `pipeline_io.go` `PrepareInput` 硬编码写 `inputs/data.csv` 并走 `VerifyCSV`（`ani.cpu.csv.v1` / 1024 行 / 16 维）。
- 文档 §7.2 要求微调 prepare 落盘 `finetune_data.json` + `dataset_info.json`（Alpaca JSON，64 条）。
- 文档 §4.2 未把 `PrepareInput` 列入修改清单——只列了 `collector.go`/`pipeline_io.go`（笼统），**没有点明 `PrepareInput` 必须按 `ExecutionKind` 分支**。  
  → **改进方向**：§4.2 明确 `PrepareInput` 按 kind 分派：`GENERAL_TRAINING` → CSV 路径（字节不变）；`FINETUNING` → JSON 路径，并复用同一套工作区根校验与 SHA/大小写入。

**【一般】2.2.3 `VerifyCSV` 与输入导入契约强绑 CPU 语义**  
`internal/biz/managed_input.go` `VerifiedCSV.ValidateFor` 硬编码 `ani.cpu.csv.v1`、1024 行、16 维；`InputImport.Validate` 拒绝 `>32MiB`。微调输入（基模 + 数据集）与这套 CSV 校验语义不同，尤其**基模不是 CSV**。  
→ **改进方向**：§2.1 基模加载需明确"基模不经 `InputImport`/`VerifyCSV`"，而是走 `ani-model-service` 的版本 + 下载 URL 通道；在文档中显式声明这两条输入通道互不复用。

**【一般】2.2.4 受管步骤白名单扩展方式未定**  
`internal/biz/managed_runtime.go` L136 白名单硬编码 5 步。文档已在 §1.2/§5/§7.3 识别需扩为 8 步，但**未说明扩法**：是"按 kind 分支返回不同白名单"还是"合并白名单 + kind 校验"。  
→ **改进方向**：明确推荐"合并白名单 + `Configuration()` 按 kind 返回期望步骤集"，并保证 CPU 分支的鉴权（`Publish` 仍按 `close`）与判定顺序不变。

**【建议优化】2.2.5 `LogicalKey` 硬编码 `"cpu-p01:"`**  
`pipeline_io.go` L174 `LogicalKey: "cpu-p01:" + execution.ExecutionID`。微调是否沿用 `cpu-p01` 前缀、是否会影响下游按前缀检索，文档未说明。  
→ **改进方向**：明确 `LogicalKey` 是否按 kind 区分（如 `lora-sft:`），若沿用则说明下游无前缀耦合。

---

## 3. 维度三：逻辑一致性

**【严重】3.1 `RequiredFiles` 相对路径 与 tar 成员名 的"反向映射"规则不自洽**  
文档 §7.4 规定：tar 成员名 = `in_tar=true` 条目**去掉 `merged/` 前缀**。但契约里的实际路径是 `export/merged/...`。若严格"去 `merged/`"，得到的是 `export/config.json`，**并非** tar 根下的 `config.json`——与 §7.4「tar 根不套层、无 `adapter/`、`reports/` 前缀」的目标矛盾。  
→ **改进方向**：把规则改为**显式映射表或"取基名（leaf）"**，即 tar 成员名 = `path.Base(RelativePath)`，并在 `output_manifest_test.go` 增断言；否则实现者会按 `TrimPrefix("merged/")` 写出错误 tar 结构。

**【一般】3.2 `ExportProgram` / `Registration` 字段在 `ReleaseDocument` 中不存在，§4.2 未列出**  
`contract/cpup01/release.go` 的 `ReleaseDocument` 结构体**无** `export_program`、`registration` 字段，且 `ParseRelease` 使用 `DisallowUnknownFields()`。文档 §4.3 模板新增了这两段，但 §4.2 修改清单只写"程序、argv、输出契约按 kind 分派"，**未明确 `release.go` 必须新增这两个字段及对应校验**。照此实施会直接解析失败。  
→ **改进方向**：§4.2 补一行：`contract/cpup01/release.go` —— `ReleaseDocument` 新增 `ExportProgram`、`Registration` 可选字段（CPU 分支为空、不影响 Canonical 字节），并补 parse/validate 分支。

**【一般】3.3 `InputRef`/`Snapshot` 无 `base_model_version_id`，新增会动 `Canonical()` 字节**  
文档 §2.1 第 4 点要求冻结快照记录 `base_model_version_id` 与物化路径摘要；但 `snapshot.go` 的 `InputRef`/`Snapshot` 均无该字段。若直接加非 omitempty 字段，**CPU 分支的 `Canonical()` 字节会变**，与"CPU 分支不变"自相矛盾。  
→ **改进方向**：明确该字段以 `json:",omitempty"` 或独立 `FineTuning *struct` 子结构承载，保证 CPU 快照规范化字节零变化，并在 `snapshot_test.go` 加"CPU 规范字节不变"回归。

**【一般】3.4 `admission_resolution` 的 values map 与所需占位符不一致**  
`internal/biz/admission_resolution.go` L200 硬编码 `Format: "CSV"`，`resolveAdmissionArguments` 的 values 仅 `INPUT_PATH/OUTPUT_PATH/INPUT_SHA256/INPUT_BYTES/LEARNING_RATE`。微调镜像加载/导出需要 `MODEL_PATH`、`ADAPTER_PATH`、`EXPORT_DIR`、`CONFIG_PATH` 等占位符，文档未把它们与 values map 的扩展对应起来。  
→ **改进方向**：§4.2 补 `admission_resolution.go` 的修改点：按 kind 扩展 values 键集 + `Format` 判定；并声明 CPU 分支键集不变。

**【一般】3.5 `registration` 段字段枚举不完整**  
跨仓核对 `ani-model-service` 的 `CreateModelVersionRequest` 实际包含 `is_encrypted`、`encrypt_algo`、`encrypt_hint`、`engine_type`、`startup_command`、`startup_args`，而文档 §4.3 registration 模板未枚举这些。微调产物若涉及加密声明或启动命令会缺字段默认值。  
→ **改进方向**：§4.3 registration 段补齐可选字段与默认值（例如 `is_encrypted=false`、`engine_type` 取值），并注明哪些是必填、哪些由 modeldev 侧兜底。

**【一般】3.6 `idempotency_key` 冲突语义表述需与实现核验**  
文档 §7.5.1（3）称 `idempotency_key` 冲突返回 **409 CONFLICT**。跨仓核对：`ani-model-service` `CreateVersion` 在 fingerprint 冲突时返回 `PgError{Code:"23505"}`，是否映射为 409 取决于 service 层的错误转换；`mapStore` 中另有 `"CONFLICT", "resource already exists"`。表述需与最终映射确认一致。  
→ **改进方向**：在 §7.5.1 注明"fingerprint 相同返回原记录（幂等成功）、fingerprint 不同返回冲突错误（映射 409）"，并指明映射点，避免实现者误判为"一律 409"。

**【建议优化】3.7 工作区子路径 `configs/` 的来源唯一性**  
§2.5 说 `configs/` 只放 prepare *派生* 配置、锁定配方固化在镜像内。逻辑清晰，但需与 D09 工作区布局（`inputs/ configs/ training/ export/ reports/ publication/`）逐项对齐，确认微调新增的 `configs/`、`export/` 与 D09 命名一致（当前命名一致，仅需在文档中引用 D09 以固化）。  
→ **改进方向**：在 §2.5 明确引用 D09 并列出"本方案子路径 ↔ D09 布局"对照表。

---

## 4. 维度四：可行性与风险点

### 4.1 技术风险

**【严重】4.1.1 多层子路径阻碍（同 2.2.1）**  
这是**唯一一条会让"照文档实施后必然跑不通"**&#x7684;问题，优先级最高。

**【严重】4.1.2 CPU 资源预算与微调负载不匹配 —— 文档未做资源调度评估**

- `pipelines/cpu_p01.py` `configure()` 固定 `cpu_request 100m / limit 500m`、`memory_request 128Mi / limit 512Mi`。
- v0.4 `cpu-baseline/README.md` 明确：微调**建议训练/导出 requests 2CPU/6Gi、limits 4CPU/8Gi、工作区 20Gi**，且注明"这是起测预算，不是已证实峰值"。
- 文档 §2/§7 未评估微调各步骤的资源覆盖（现有 500m/512Mi 会直接 OOM/超时），也未说明 `finetune_cpu.py` 如何按步骤设置 request/limit。  
  → **改进方向**：新增"§ 资源与调度"章节：给出 prepare/train/export/verify 各步 request/limit 建议值、工作区 PVC 容量（≥20Gi）、超时与重试策略；并声明这些是"起测预算，待 `finetune_proposal_tested` 实测校准"。

**【一般】4.1.3 KFP 执行环境可能无 GPU / 需强制 CPU**  
配方 `finetune_cpu.yaml` 有 `use_cpu: true`，但文档未说明 KFP 节点是否需要 nodeSelector/toleration 保证落到 CPU 池，以及 CPU 下 LLaMA-Factory 的预期耗时（可能远超 KFP/步骤超时）。  
→ **改进方向**：在资源章节补 nodeSelector、步骤超时与"CPU 下预期耗时量级"。

**【一般】4.1.4 基模下载 URL 的 5 分钟 TTL 与长任务**  
`ani-model-service` `GetModelDownloadURL` TTL 仅 5 分钟。若 prepare 阶段先取 URL、实际下载在 train 阶段之后，URL 可能过期。  
→ **改进方向**：明确"URL 取用即下载"或"每阶段重新获取"，并在 §7.5.1 注明 TTL 约束。

**【一般】4.1.5 `output_verifier` 三重冲突的修法需与"剥离前缀"协同**  
文档 §7.4 已识别 L20/L26/L38/L49 四处（拒绝 manifest 名、无条件加 manifest、精确名匹配、计数相等）。但修法必须与 **3.1 的 tar 成员名映射规则**一致，否则"tarball 成员名 ↔ RequiredFiles 相对路径"的对应关系仍会错。  
→ **改进方向**：把 §7.4 的 verifier 修改与 tar 命名规则合并成一节，给出统一映射函数与断言。

### 4.2 边界场景缺失

**【一般】4.2.1 空产物 / 部分产物的处理**  
若 export 成功但 verify 失败，已发布的 adapter 对象与 tar 是否保留、如何标记。文档只在 §7.4 说"发布失败不删 PVC"，未覆盖"verify 失败但 publish 已发生"。  
**【一般】4.2.2 同名/重复 `CreateModelVersion` 的重试窗口**：跨仓幂等比对 `RequestFingerprint`，文档未说明 fingerprint 由哪些字段构成、微调侧如何生成。  
**【建议优化】4.2.3 大模型文件大小上限**：`RequiredOutput.MaxSizeBytes` 与 `OutputManifest` 的大小校验对 >100MiB 的 `model.safetensors` 是否有上限/分片约定，文档未提。

### 4.3 可维护性

**【建议优化】4.3.1 条件分支散落**  
"CPU 分支不变"的判定会散布到 snapshot/release/intent/collector/pipeline_io/admission 等处，建议集中为 `kind` 驱动的策略表（strategy/registry），降低未来加 kind 的成本。  
**【建议优化】4.3.2 硬编码常量应外提**  
`Program.AllowUnsafe`、CPU command 数组、`ani.cpu.mlp.parameters.v1` 等应纳入 capability 常量表，避免与微调分支各写一份。

---

## 5. 维度五：改进方向汇总（按优先级）

| # | 问题                                                          | 等级 | 文档是否已识别               | 改进方向                                              |
| - | ----------------------------------------------------------- | -- | --------------------- | ------------------------------------------------- |
| 1 | `collectFile`/`capturePipelineFile` 不支持多层相对路径               | 严重 | **否（遗漏）**             | §4.2 补两处修改；保持防穿越；加负向用例                            |
| 2 | `PrepareInput` 硬编码 `data.csv`/`VerifyCSV`                   | 严重 | **否（遗漏）**             | §4.2 补 `PrepareInput` 按 kind 分支                   |
| 3 | tar 成员名"去 `merged/` 前缀"规则不自洽                                | 严重 | 部分（§7.4 有方向但规则错）      | 改为 leaf/显式映射表，加断言                                 |
| 4 | 微调资源预算与 `cpu_p01.py` 固定限额不匹配                                | 严重 | **否（遗漏）**             | 新增"资源与调度"章节，按 D09/README 给预算                      |
| 5 | `ReleaseDocument` 缺 `export_program`/`registration` 字段未列入修改 | 一般 | 否（§4.2 未列 release.go） | §4.2 补 `release.go` 新增字段 + parse/validate         |
| 6 | `InputRef` 新增 `base_model_version_id` 会动 Canonical 字节       | 一般 | 部分                    | 用 omitempty/子结构；加"CPU 字节不变"回归                     |
| 7 | `admission_resolution` values/Format 未按 kind 扩展             | 一般 | 否                     | §4.2 补修改点，声明 CPU 键集不变                             |
| 8 | registration 段字段枚举不全                                        | 一般 | 否                     | 补齐 `is_encrypted/encrypt_*/engine_type/startup_*` |


| 9 | `idempotency_key` 冲突语义表述需与实现核验 | 一般 | 部分 | 明确 fingerprint 语义与 409 映射点 |  
| 10 | 基模下载 URL 5 min TTL vs 长任务 | 一般 | 否 | 明确取用时机 |  
| 11 | 异常处理不成体系（缺失败路径章节） | 一般 | 否 | 新增失败路径与状态迁移表 |  
| 12 | 验收标准缺量化断言/C 编号 | 一般 | 否 | 补 C-FT 用例与判据 |  
| 13 | 受管步骤白名单扩展方式未定 | 一般 | 部分 | 明确"合并白名单 + kind 期望集" |  
| 14 | `LogicalKey` 硬编码 `cpu-p01:` | 建议 | 否 | 说明是否按 kind 区分 |  
| 15 | 条件分支散落 / 常量硬编码 | 建议 | 否 | 策略表 + 常量表 |  
| 16 | 空产物/部分产物、大文件上限等边界 | 建议 | 否 | 补边界与上限约定 |

---

## 6. 文档已正确识别、无需返工的偏差（确认项）

以下偏差文档**已主动识别并列入修改清单**，复核确认其判断正确，仅需按清单实施即可：

1. `RequiredOutput` 新增 `in_tar` 字段（proto + Go 结构 + codec 双向映射）—— 契约确无此字段，属纯新增。
2. `WorkspaceContract` 新增 `configs/`、`export/` 子路径并纳入 `invalidWorkspaceContract` 重叠校验 —— 现仅 4 项。
3. 受管步骤白名单由 5 步扩为 8 步（`export/verify/register`）—— `managed_runtime.go` L136 确为硬编码。
4. `output_verifier.go` 的 manifest 冲突（L20/L26/L38/L49）—— 已核实。
5. 新增独立 `pipelines/finetune_cpu.py`、`cpu_p01.py` 零改动 —— 与 §7.3 一致。
6. Model 登记调用链（`GetModelVersion`/`GetModelDownloadURL`/`CreateModel`/`CreateModelVersion`）与 model-service 实际 RPC 一致。
7. `ani.metric.v1` 为训练负载运行时 JSONL schema（非 proto 契约）—— 文档引用正确，本仓有对应测试佐证。
8. `finetune_proposal_tested: false` —— 与文档 §6"未验证"自洽。

---

## 7. 建议的最小落地顺序（对齐 §7.6 B1–B5）

1. **B0（前置，本次复核新增）**：先修 `collectFile`/`capturePipelineFile` 多层路径 + `PrepareInput` 按 kind 分支 + 资源限额注入——这三项是"能否跑通"的地基。
2. **B1**：契约层（proto `in_tar`、`WorkspaceContract` 子路径、`ReleaseDocument` 新字段、`InputRef` 基模字段 omitempty）。
3. **B2**：受管步骤白名单 + `managed_steps` 三步扩展 + 鉴权保持。
4. **B3**：`pipelines/finetune_cpu.py` + `component/export_verify.go`。
5. **B4**：tar 入包子集/命名映射 + `output_verifier` 放开 + 双重发布说明。
6. **B5**：publish → register 调用链 + 幂等 + 失败路径。

> 每批完成后跑一次"CPU 分支 `spec_hash` 字节不变"回归，作为拆分改造的安全网。
