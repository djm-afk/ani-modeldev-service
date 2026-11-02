# CPU-P01 执行快照规范 v1

历史 CPU-P01 v1 设计记录；当前执行快照为 `ani.modeldev.execution-spec.v4`，
动态环境与有效部署入口见[当前合同](../modeldev-tenant-environments.md)。
下文旧 Release/facts/快照形状不作为当前配置或兼容入口。

本文件细化 [最小领域合同](cpu-p01-domain-model.md) 的 `execution_spec_hash`。
源合同是 CPU01、CPU04 及 v0.4 development D03、D05、D08、D13；字段与
`api/ani/modeldev/v1/modeldev.proto` 的 ExecutionSnapshot 对齐。

这是受理时完整解析的配置，不是运行记录。Governance 必须在可靠受理事务中把
operation、execution、可信 resource tenant UUID、可信 actor、intent_hash、
snapshot 和 execution_spec_hash 一起固定。ModelDev 持久接收时逐项验证并绑定。
Snapshot 自身不包含 tenant/actor/operation/execution，因此相同配置可以有相同
spec hash；这不允许跨租户接管，也不替代 operation/execution 唯一约束和授权。

`deadline_at`、已有 namespace UID、环境绑定 digest、受管身份和存储引用均进入
spec hash。受理后产生的 Run/Workflow/PVC/TrainJob/JobSet/Pod UID、状态、观察时间、
上传回执、短期 URL/token 均不进入快照。旧快照不会因为 deadline 已经过期而失去
可验证性；创建路径另按当前时间检查原截止时间，重试不能修改它。

## 规范字节

`execution_spec_hash = lowercase_hex(SHA256(snapshot.Canonical()))`。
字节为 UTF-8，无 BOM、无尾随换行，无多余空白。根字段严格依次为：

`schema_version, kind, delivery_mode, release, input, program, resources,
environment, workspace, publication_scope, output_contract, deadline_at`。

schema_version 固定 `ani.modeldev.execution-spec.v1`；kind 固定
GENERAL_TRAINING；delivery_mode 固定 SAVE_ARTIFACTS。所有非可选字段始终写入。

嵌套字段按下表顺序写入，名称与 Proto/Go JSON tags 一致：

| 对象 | 字段顺序 |
|---|---|
| release | release_id, release_digest, preset_id, accepted_binding_generation, pipeline_id, pipeline_version_id, pipeline_ir_sha256, runtime |
| runtime | name, kind, api_group, content_sha256, target_jobs |
| input | input_version_id, object, format, schema_version, row_count, feature_count |
| object | storage_connection_id, bucket, key, version_id 或 immutable_copy, size_bytes, sha256 |
| program | image_version_id, image_digest, command, resolved_args, resolved_parameters |
| parameter | name, type, value |
| resources | nodes, processes_per_node, request_millicpu, limit_millicpu, request_memory_bytes, limit_memory_bytes |
| environment | binding_id, binding_digest, cluster_id, namespace_name, namespace_uid, kfp_connection_ref, experiment_id, identities |
| identities | modeldev_control_identity_ref, kfp_step_service_account, trainer_service_account, verifier_service_account, tenant_proxy_identity |
| workspace | mode, storage_class, capacity_bytes, input_subpath, training_subpath, reports_subpath, publication_subpath |
| publication_scope | storage_connection_id, bucket, approved_prefix, credential_reference |
| output_contract | schema_version, output_kind, delivery_mode, required_files, max_file_count, max_total_bytes, create_tar_bundle |
| required_file | role, relative_path, max_size_bytes |

Release/preset/Pipeline/InputVersion/image/binding/Namespace UID/Experiment 的
UUID 规范化为小写；cluster_id 是 ENV 的稳定标识，按引用原字符串保存，不能要求
环境为通过本地校验另造 UUID。SHA256 必须是 64 位小写十六进制；image_digest 是完整
`registry/repository@sha256:<64位摘要>`，不得是浮动 tag。所有 int64 及 uint64
写成规范十进制 JSON 字符串（与 ProtoJSON 一致），uint32 写成无前导零数字。
deadline 转 UTC RFC3339Nano，零小数秒省略，非零小数秒去尾零。

JSON 字符串沿用 intent v1 的转义：不 HTML 转义 `<>&`，U+2028/U+2029 转义，
其余非控制字符原样 UTF-8。不得静默裁剪名称、身份引用、路径或存储 key。

三个集合排序：resolved_parameters 按 ASCII name 升序，runtime.target_jobs 按
ASCII 名称升序，required_files 按相对路径字节升序。重复项拒绝。command 和
resolved_args 顺序有意义，不排序。全部排序操作使用副本，不修改调用者切片。
resolved_parameters 必须完整包含 epochs INTEGER=3、batch_size INTEGER=64、
learning_rate DECIMAL (0,0.1]；小数去尾零，不能再次查询默认值。

快照的非可选 repeated 字段不携带用户意图的 presence：合法的空
program.resolved_args 在 Go 中无论 nil 还是空切片，都规范化为 JSON `[]`，
不能输出 null。这保证 protobuf repeated 字段经过真实 wire 后摘要不变。
runtime.target_jobs、command、resolved_parameters 和 required_files 仍须满足
各自非空校验，不因这条编码规则放宽。UserIntent 的 general_parameters 缺省
和显式空数组仍有不同意图摘要，继续使用可选 ParameterSelection 保留。

固定对象必须恰有一个不可变依据：非空 version_id 或 immutable_copy=true。
缺失、两者同时存在、immutable_copy=false 均拒绝。该字段是批准配置的事实，
本地校验不能证明对象不可覆盖；CPU04 输入导入仍必须固化并读取实际字节验证。

## 校验与证据边界

Snapshot.Validate 用于受理和持久接收之前的纯本地结构校验：要求完整固定的
Release/Pipeline/input/image/Runtime/环境引用，固定单节点单进程 CPU 规格，
受限且不重叠的工作子目录、允许的输出合同和稳定存储引用。UNKNOWN、NOT_READY、
UNSET、空值、零 UUID、浮动镜像、缺少内容摘要的 Runtime 和可变输入均不合法。

绑定 generation 必须大于零；deadline 必须非零、年份为 1–9999 且能表示为 RFC3339，不能按本地
当前时钟拒绝历史快照。deadline 晚于 accepted_at 的跨字段关系由受理 envelope
校验，accepted_at 不重复塞入 Snapshot。CPU request 必须正数，limit 不小于
request，nodes/processes 均为 1。三个工作负载 SA 必须各不相同，训练与 verifier
不能获得受管步骤身份；具体 RBAC/网络权限仍须真实鉴权验证。

本片 CSV schema 固定 ani.cpu.csv.v1、1024 行、16 特征；输出 schema 固定
ani.cpu.output.v1。required_files 必须包含且仅包含 model.pt/CHECKPOINT、
model_config.json/MODEL_CONFIG、metrics.jsonl/METRICS、summary.json/SUMMARY。
路径/role 不得重复替换，每文件限额为正且不大于总限额，max_file_count 至少 4。
工作子目录为规范相对 POSIX 路径且互不相同、互不包含；批准存储前缀不得为空、
根路径、越界路径或通配表达式。S3 的字符串 null 版本不构成固定版本依据。

环境启用、对象存在、镜像可拉取、Runtime 实际 CRD、Namespace UID、证书/SA 权限、
存储读写和 PipelineVersion 真实性由 CPU02/04/07 的受管事实验证提供。纯本地
Validate 成功不可以将候选 Release/环境置 READY。不能用格式正确的随机 UUID
替代 ENV 交接，不能把 fixture 作为生产默认值。

共享测试中的 registry.example.test、示例身份与重复十六进制摘要仅为规范字节
向量；不是现存物料，也不得被发布目录当作已验收证据。首次正向向量逐字保存在
snapshot_test.go，SHA 必须由 Fedora Python hashlib 独立计算并归档。

## 实施状态

固定 f55fb60 已在 Fedora 对首次 canonical tracer 取得预期 RED（未实现）。
固定 0fd3ae0 在 Fedora 取得合同测试 GREEN，证据为
evidence/cpu01-snapshot-green-0fd3ae0.txt。model.pt 替换后规范向量已修正为
路径升序（metrics.jsonl 在 model.pt 前）；Fedora Python hashlib 独立计算得到
`972dee14e65202d5d4da7da199cf5f37139b535701a0cb1b3de4d8ef8a5170b9`。
新测试保存该精确摘要，并对 Validate/Canonical/Digest 三个公共边界加入无效
固定配置反例。固定 0a93b04 已在 Fedora 取得预期 RED，证据为
evidence/cpu01-snapshot-validation-red-0a93b04.txt。
现已实现本地结构校验并接入 Canonical/Digest；任何非法快照均应返回
INVALID_ARGUMENT，不产生规范字节或摘要。错误只写固定字段类别，不回显配置值。
CPU04 可以复用这一纯本地边界，仍须验证受理 envelope 与可信外部事实。
固定 87b28548786443431e32fa53a22176646a30c3b2 的 Fedora 合同测试已通过，
包括已实现的纯快照校验及 protobuf 正负 roundtrip。空 resolved_args 跨 wire 的
规范化回归在固定 870994e29f28e170eef6314fd09569e6d7728ac7 取得预期 RED：
nil 输出 null，显式空数组跨 wire 后摘要变化；managed-copy oneof roundtrip 已通过。
证据为 evidence/cpu01-empty-args-red-870994e.txt/.exit。现仅在 Canonical 中将
resolved_args 的副本统一为空数组，等待固定提交的 Fedora GREEN。不能因此标记
整个 CPU01 CODE_READY。
