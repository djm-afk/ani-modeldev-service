# 受管解析事实：已发布动态环境

当前入口是 `internal/data/admissionfacts.Reader.ReadAdmissionFacts`。
旧 `FileSource`、`facts_files` 和 `ani.modeldev.managed-admission-facts.v1`
文件入口已经移除，不能作为配置或 READY 来源；不提供兼容加载。
平台部署合同见[动态租户执行环境](../modeldev-tenant-environments.md)。

Reader 仅持有环境登记只读 port、live 身份校验 port、四份受管材料 reader 和
固定 cluster ID。它按可信 tenant＋cluster 读取已持久 READY 的 sealed binding，
核对规范字节、tenant/cluster、四 Pipeline/Version/IR/material 身份及 workspace，
再读取现有 Namespace 与三种 SA，比较原 UID、owner 和精确 pull refs。
未登记返回 `ENVIRONMENT_NOT_REGISTERED`；失败不会触发 Ensure 或创建资源。

返回值复用 `biz.TenantAdmissionFacts`：Environment、InputScope、ModelScope、
PublicationScope、Workspace、Timing、TargetPlatform、DispatchOwner、
ManagedImageAccess 和实现对应的绝对输入/输出路径。环境绑定来自
`ani.modeldev.tenant-execution-environment.v1` 完整规范投影；摘要没有自身字段。
这些值不是用户提供的 facts，也不是声明 VERIFIED 的配置开关。

输入、发布和基模 scope 分别由同一平台 connection/bucket 模板派生为
`inputs/<tenant>`、`executions/<tenant>`、`<tenant>`。DispatchOwner 在环境层固定
`s3://<artifact bucket>/artifacts/<tenant>`；Prepare 仅追加
一次 `/<execution UUID>`，Finalize 重读当前 READY 环境并核对候选中的原值。
实际字段、规范化和纯结构约束以
`contract/tenantenvironment/binding.go`、`internal/data/admissionfacts/reader.go`
和 `internal/biz/admission_resolution.go` 为准。

事实读取是只读操作，既不授予业务受理、Reserve、训练创建或对象存储写权限，也不
改变关闭 fence 和原占额释放。新调用仍须完成 Governance 当前授权；已受理执行的
关闭依据原快照，不能因为环境当前 BLOCKED 而丢失原资源关闭能力。

验证分开记录：真实 PostgreSQL＋生产 Ensure 的外部边界替身验证，证明持久化、
seal→投影→CAS 和读件关联；真实集群的 Namespace/SA 原 UID、bootstrap、材料、
artifact I/O、普通租户授权和 A/B 训练必须另附现场证据。组件测试不构成 LIVE PASS。
