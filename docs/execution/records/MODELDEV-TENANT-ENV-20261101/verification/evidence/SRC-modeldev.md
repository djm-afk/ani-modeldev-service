# ModelDev main 源码核验（SOURCE）

采集时间（UTC）：2026-10-31T14:39:25Z–2026-10-31T14:52:04.962299Z
执行环境：本地只读 Git/轻量分析；工作目录 `/home/chabking/workspace/ani-modeldev-service`。
远端 `main`：`21bf4bd6540d462a61c91b629f26250899583223`（git ls-remote 实测）；本地 HEAD：`5b7fd05d9a74a8d0c65577e71b9a5c2db5dc8163`。

主链：受信租户 → 租户 Namespace/UID → Environment/Experiment/身份 → 固定 PipelineVersion 的 Run → 回调 → execution workspace/Trainer → close/cleanup/GPU release outbox。当前源码断点是单 tenant/environment 的进程 binding、凭据与后台发现，以及静态 facts/准入/STS 材料；本次只核验，没有接通新租户主链。

证据等级全部为 **SOURCE**。以下 PASS 只表示该项源码约束得到确认；FAIL 表示当前源码明确不满足动态多租户或统一 Namespace 目标。现场镜像映射、当前配置、权限、已有执行和新执行行为全部另列未核验，不能把本文件当作现场通过证据。

工作区初始与结束状态相同：
```text
 M AGENTS.md
?? .scratch/
?? docs/agents/
?? examples/
?? os,subprocess,json,hashlib,time,stat,struct,traceback
```

只执行 Git 元数据查询、固定对象读取及一次 `fetch --no-tags --no-write-fetch-head origin <SHA>`；首次对象不存在的 exit 128 和后续取得对象的 exit 0 均保留在结构化命令日志。没有 build/test、SSH/集群连接、凭据文件读取、初始化、资源修改、数据库操作或代码提交。

## SRC-MD-01 — 固定远端 main 与保留工作区

检查：`K00.SOURCE`；状态：**PASS / SOURCE**。

git ls-remote origin refs/heads/main 返回 21bf4bd6540d462a61c91b629f26250899583223；本地 HEAD 为 5b7fd05d9a74a8d0c65577e71b9a5c2db5dc8163，开始和结束均存在相同未提交路径。所有结论使用 git show/git grep 固定远端 SHA。远端对象开始缺失（exit 128），随后仅 fetch 对象（exit 0），没有切换分支或覆盖工作区。

实施影响：源码与部署版本必须分别报告。

缺口：SOURCE_IMAGE_MAPPING_NOT_VERIFIED；没有读取现场组件或制品映射。

## SRC-MD-02 — 进程仅装配一个 PipelineDispatchBinding

检查：`K02.SOURCE.BINDING`；状态：**FAIL / SOURCE**。

runtimeClients.binding 是单值；loadRuntimeClients 从 BindingFile 读取并核对 SHA256，解码单个 PipelineDispatchBinding。binding 冻结 TenantID、Environment、Owner；Environment 含 cluster、namespace/UID、Experiment、连接与身份引用。该值传给一个 dispatcher 和一个 close worker。

实施影响：仅增加第二租户 admission facts 不会使第二租户被同一进程派发、关闭或处理 GPU release outbox；需一次性替换静态 binding 发现与装配。

源码：
- [`cmd/ani-modeldev-service/runtime_clients.go:33–58`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/cmd/ani-modeldev-service/runtime_clients.go#L33-L58)
- [`internal/biz/dispatch_worker.go:15–50`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/dispatch_worker.go#L15-L50)
- [`contract/cpup01/snapshot.go:95–110`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/contract/cpup01/snapshot.go#L95-L110)
- [`cmd/ani-modeldev-service/runtime.go:117–145`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/cmd/ani-modeldev-service/runtime.go#L117-L145)

缺口：现场 binding 文件、SHA、具体租户、Experiment、身份引用均未读取。

## SRC-MD-03 — mountedPipelineToken 拒绝不同租户或整份 environment

检查：`K02.SOURCE.KFP_TOKEN, K04.SOURCE.CONTROL`；状态：**FAIL / SOURCE**。

provider 保存启动 binding 和 token 文件路径；BearerToken 要求 tenant == binding.TenantID 且 environment == binding.Environment，然后每次读取 runtimeToken(file)。它不会按 TenantProxyIdentity 交换凭据。KFP CA 和 token 文件路径来自受管配置。

实施影响：控制身份/刷新方案须依据真实 KFP 入口认证选择；不能把文件轮转误报成动态租户凭据映射。

源码：
- [`cmd/ani-modeldev-service/runtime_clients.go:72–85`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/cmd/ani-modeldev-service/runtime_clients.go#L72-L85)
- [`cmd/ani-modeldev-service/runtime_clients.go:129–129`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/cmd/ani-modeldev-service/runtime_clients.go#L129-L129)
- [`cmd/ani-modeldev-service/runtime_clients.go:152–178`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/cmd/ani-modeldev-service/runtime_clients.go#L152-L178)

缺口：真实 control principal、audience、token 有效性、CA 引用值、入口层均 NOT_VERIFIED；未读取 token/CA/私钥文件。

## SRC-MD-04 — step 回调 audience 与 KFP control 认证分离

检查：`K04.SOURCE.CALLBACK`；状态：**PASS / SOURCE**。

runtime.token_audience 仅传给 stepidentity.New。回调 verifier 提交显式 audiences 的 TokenReview，并要求 authenticated、返回 audience、Pod name/UID、精确 SA principal 和 SA UID；再 GET Namespace/Pod/SA/Workflow 核对原 UID、删除状态及 owner。

实施影响：动态化应保留 Pod、SA、Workflow、Namespace 的原实物绑定。runtime.token_audience 不能作为 ModelDev→KFP 实际 audience 证据。

源码：
- [`cmd/ani-modeldev-service/runtime.go:84–91`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/cmd/ani-modeldev-service/runtime.go#L84-L91)
- [`internal/data/stepidentity/verifier.go:29–95`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/stepidentity/verifier.go#L29-L95)

缺口：现场 projected token 的 audience、expirationSeconds、volume 与路径关系 NOT_VERIFIED。

## SRC-MD-05 — CreateRun 由 Experiment 与固定 Version 决定执行材料

检查：`K05.SOURCE.CREATE_RUN, K06.SOURCE.FIXED_VERSION`；状态：**PASS / SOURCE**。

KFP client 将 base URL 原路径后拼 /apis/v2beta1/runs。CreateRun body 仅含 experiment_id、display_name、pipeline_version_reference（PipelineID/VersionID）、runtime_config（execution_id/spec_hash 与 pipeline_root）、service_account；无独立 Namespace。Authorization 仅由 TokenProvider 生成，不接受浏览器头映射。HTTPS、受管 CA、有限超时、不跟随跳转、无重试；响应须 200 JSON 且完整匹配原请求和非零 Run UUID，含 error 则不确认。

实施影响：不能从请求体推断实际 Workflow namespace，也不能从固定 ID 推断共享/租户归属或 B 可使用；必须补 KFP 现有对象 GET、权限与执行证据。

源码：
- [`internal/data/kfp/client.go:66–99`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/kfp/client.go#L66-L99)
- [`internal/data/kfp/client.go:102–162`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/kfp/client.go#L102-L162)
- [`internal/data/kfp/client.go:178–197`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/kfp/client.go#L178-L197)
- [`internal/data/kfp/client.go:244–261`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/kfp/client.go#L244-L261)

缺口：当前 Pipeline/Version ID、父 Pipeline namespace/共享属性、B 精确读取、runs/create 权限和已有 B Run 均 NOT_VERIFIED。

## SRC-MD-06 — admission facts 支持多个键但为启动不可变快照

检查：`K02.SOURCE.ADMISSION_FACTS`；状态：**FAIL / SOURCE**。

command 构造时从 config.AdmissionResolution.FactsFiles 调用 admissionfacts.Load。Reader 按 resource_tenant_id + implementation_id 索引文件，限制 1..64，拒绝重复键，要求所有文件 timing 相同；ReadAdmissionFacts 只查已加载 map，不做环境探测或动态登记。

实施影响：动态开通须替换启动 facts 的静态来源，同时保留严格文件/摘要、受信租户和已冻结执行快照语义。

源码：
- [`cmd/ani-modeldev-service/command.go:100–125`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/cmd/ani-modeldev-service/command.go#L100-L125)
- [`internal/data/admissionfacts/reader.go:26–30`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/admissionfacts/reader.go#L26-L30)
- [`internal/data/admissionfacts/reader.go:72–132`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/admissionfacts/reader.go#L72-L132)
- [`internal/data/admissionfacts/reader.go:134–194`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/admissionfacts/reader.go#L134-L194)

缺口：现场 facts 文件数量、租户数量、材料摘要和具体存储 scope 未读取。

## SRC-MD-07 — 派发发现受单 tenant/environment 过滤，Reserve 是唯一发送许可

检查：`K02.SOURCE.DISPATCH, K02.SOURCE.RESERVE`；状态：**FAIL / SOURCE**。

ListPendingAdmissions 要求指定 tenant UUID、snapshot.environment 与参数 JSON 完全相等、close_generation=0、未到 deadline、无既存 dispatch。DispatchWorker 还逐项核对同一 tenant/environment。Reserve 对共享 identity 行 FOR UPDATE，比较持久 admission/完整原计划；已有 reservation 仅回放事实、不返回 SendPermit；只有首次插入成功且事务提交后返回唯一许可。

实施影响：改发现范围时沿用 Reserve、关闭 fence、deadline 与发送不确定性，不能新增重试系统绕过。

源码：
- [`internal/data/submission/queries.sql:210–233`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/submission/queries.sql#L210-L233)
- [`internal/biz/dispatch_worker.go:69–89`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/dispatch_worker.go#L69-L89)
- [`internal/biz/dispatch_worker.go:105–128`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/dispatch_worker.go#L105-L128)
- [`internal/data/submission/repository.go:59–106`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/submission/repository.go#L59-L106)
- [`internal/data/submission/repository.go:111–150`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/submission/repository.go#L111-L150)
- [`internal/biz/pipeline_submitter.go:50–86`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/pipeline_submitter.go#L50-L86)

缺口：未执行数据库行为测试或新派发；SOURCE 不证明现场可用。

## SRC-MD-08 — 关闭与 GPU release outbox 发现同样固定范围

检查：`K02.SOURCE.CLOSE_OUTBOX`；状态：**FAIL / SOURCE**。

ListPendingBoundClosures 要求 admitted.tenant_id 和 snapshot.environment 完全匹配，覆盖关闭 fence、deadline/step 超时、未完成 compute cleanup 以及已关闭但 GPURelease.outbox.delivered_at 为空等条件；CloseWorker 使用同一 binding.TenantID 调 closer/GPU reconciler。

实施影响：只修改提交路径会遗漏第二租户关闭、清理和 GPU release outbox，必须同次替换发现范围。

源码：
- [`internal/data/lifecycle/queries.sql:78–128`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/lifecycle/queries.sql#L78-L128)
- [`internal/biz/close_worker.go:9–41`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/close_worker.go#L9-L41)
- [`internal/biz/close_worker.go:47–86`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/close_worker.go#L47-L86)

缺口：现场待关闭或待投递记录、GPU释放状态未查询。

## SRC-MD-09 — webhook 为静态配置，Namespace 租户标签仍使用旧键

检查：`K08.SOURCE.LABELS, K08.SOURCE.WEBHOOK_LIMIT`；状态：**FAIL / SOURCE**。

webhook Config 有 bindings/profiles；freeze 限 bindings 1..32、profiles 1..64；每个 binding 固定 namespace/UID、owner label、Argo creator、step/training SA、profile。Handler.New 将其复制入 map。NamespaceTenantLabel 固定为 ani.io/resource-tenant-id，step/training 会核对该键。

实施影响：需改成统一 network.ani.io 合同并替换静态绑定来源；32 是源码上限，不是现场加载数量。

源码：
- [`internal/server/modeldevwebhook/config.go:31–58`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/config.go#L31-L58)
- [`internal/server/modeldevwebhook/config.go:93–104`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/config.go#L93-L104)
- [`internal/server/modeldevwebhook/config.go:162–166`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/config.go#L162-L166)
- [`internal/server/modeldevwebhook/config.go:209–242`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/config.go#L209-L242)
- [`internal/server/modeldevwebhook/handler.go:32–64`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/handler.go#L32-L64)
- [`internal/server/modeldevwebhook/policy.go:233–245`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/policy.go#L233-L245)
- [`internal/server/modeldevwebhook/training.go:71–82`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/training.go#L71-L82)

缺口：实际 Namespace labels、bindings 数量、webhook 配置未读取。

## SRC-MD-10 — 启用 training 的注册会命中 Namespace 全部 Pod CREATE

检查：`K08.SOURCE.WEBHOOK_REGISTRATION`；状态：**FAIL / SOURCE**。

generator 为每个 binding 产生 Pod CREATE、Namespaced、Exact 规则，namespaceSelector 同时要求 kubernetes.io/metadata.name 和配置 owner label；初始 objectSelector=v2_component:true，training 存在时删除该 selector；failurePolicy=Fail、timeoutSeconds=8，无 matchConditions。handler 对非 training SA 且非 v2_component、无 ModelDev 自报角色标签的普通 Pod 返回 outside_scope/Allowed。

实施影响：handler 放行逻辑不能免除调用前故障影响；后续需收窄实际注册，不能用 Ignore 代替工作负载校验。

源码：
- [`deploy/modeldev-webhook/render.py:76–83`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/deploy/modeldev-webhook/render.py#L76-L83)
- [`internal/server/modeldevwebhook/policy.go:202–224`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/policy.go#L202-L224)

缺口：没有读取现存 MutatingWebhookConfiguration，不能称现场普通 Pod 已受阻；PSA/网络/Quota/LimitRange 均未核验。

## SRC-MD-11 — 任一 binding 的 Namespace 查询或 UID/owner 校验失败可使共享 readiness 失败

检查：`K08.SOURCE.READINESS`；状态：**FAIL / SOURCE**。

/readyz 调用 handler.ready；ready 遍历全部已加载 binding，逐个 GET Namespace；错误、缺失、名称/UID 不符、删除中或 owner label 不匹配均立即 false。它使用一个共享 dependency timeout。

实施影响：共享服务需拆开单租户环境状态与实例就绪条件，避免一个租户问题影响全部匹配 Namespace 的准入可用性。

源码：
- [`internal/server/modeldevwebhook/handler.go:77–88`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/handler.go#L77-L88)
- [`internal/server/modeldevwebhook/handler.go:103–114`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/server/modeldevwebhook/handler.go#L103-L114)
- [`deploy/modeldev-webhook/render.py:68–74`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/deploy/modeldev-webhook/render.py#L68-L74)

缺口：现场 readiness、Pod UID、RBAC 与实际故障影响未验证。

## SRC-MD-12 — TrainJob policy/binding 固定全局名称且单 Namespace 内容

检查：`K08.SOURCE.POLICY`；状态：**FAIL / SOURCE**。

generator 固定 NAME=modeldev-v21-trainjob-01a15f47，控制/Trainer/GC principal 也为常量；render 输入单 namespace/namespace_uid 与固定 CPU/GPU digest。Policy 与 Binding 同用 NAME，namespaceSelector 仅该 namespace，匹配 trainer.kubeflow.org/v1alpha1 TrainJob/TrainJob status CREATE/UPDATE；Deny/Fail。租户键仍是 ani.io/resource-tenant-id；Namespace UID label 只是 scope marker，注释明确不能作为实际 UID 证明。

实施影响：为第二租户再次套用同名全局对象会覆盖前一租户配置内容（源码生成碰撞风险）；需一次性改多租户策略设计/命名，保留实际 Namespace UID 读取。

源码：
- [`deploy/modeldev-tenant-policy/render.py:13–31`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/deploy/modeldev-tenant-policy/render.py#L13-L31)
- [`deploy/modeldev-tenant-policy/render.py:47–50`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/deploy/modeldev-tenant-policy/render.py#L47-L50)
- [`deploy/modeldev-tenant-policy/render.py:66–74`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/deploy/modeldev-tenant-policy/render.py#L66-L74)

缺口：实际 Policy/Binding 名称与内容、Kubernetes 支持版本未读取；没有执行 render/apply/server dry-run。

## SRC-MD-13 — workspace 与清理使用执行专属名称、原 UID 和 owner 关系

检查：`K08.SOURCE.WORKSPACE_CLEANUP`；状态：**PASS / SOURCE**。

ExpectedWorkspacePVCName 为 ani-kfp-workspace-<lowercase execution UUID>，实际 PVC UID 仅在创建/准备后冻结。ValidateWorkspaceBinding 要求 Namespace name/UID、非空 PVC name/UID、子路径和 prepared manifest 匹配。owner close 扫描 Pod 后跳过既非原 Workflow owner、也不挂该 workspace 的 Pod；挂同 workspace 的外部 Pod 不被跳过且非 terminal 则阻止证明。compute cleanup 校验 Namespace/PVC 原 UID，逐个按记录目标删除并使用 UID/resourceVersion precondition；未知 owner 子对象/挂 workspace Pod 会阻止清理。

实施影响：复用租户 Namespace 不要求无关 Instance/VM 消失；应保留精确执行工作区/原对象 UID 边界，不扩大到 Namespace 清空。

源码：
- [`internal/biz/runtime_plan.go:17–21`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/runtime_plan.go#L17-L21)
- [`internal/biz/execution_runtime.go:379–391`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/execution_runtime.go#L379-L391)
- [`internal/data/runtimeproof/owner_close.go:58–83`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/runtimeproof/owner_close.go#L58-L83)
- [`internal/data/runtimeproof/owner_close.go:115–154`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/runtimeproof/owner_close.go#L115-L154)
- [`internal/data/runtimeproof/owner_close.go:582–602`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/runtimeproof/owner_close.go#L582-L602)
- [`internal/data/runtimeproof/compute_cleanup.go:94–118`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/runtimeproof/compute_cleanup.go#L94-L118)
- [`internal/data/runtimeproof/compute_cleanup.go:183–218`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/runtimeproof/compute_cleanup.go#L183-L218)
- [`internal/data/runtimeproof/compute_cleanup.go:330–349`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/runtimeproof/compute_cleanup.go#L330-L349)

缺口：现场 PVC/Workflow/JobSet/Job UID 与共享挂载情况未读取；源码不证明已有执行已关闭。

## SRC-MD-14 — 通用训练走 Res pull-secret；固定微调保留自身模板路径

检查：`K09.SOURCE.IMAGE`；状态：**PASS / SOURCE**。

通用 TrainingImage 非空时 RuntimePlan 冻结 imagePullSecrets 引用；ensurePullSecret 调 Res 并传受信 tenant、cluster、namespace、原 namespace UID、registry，比较返回 Secret ref。微调没有 TrainingImage 时不进入此路径；训练模板不显式携带自己的 pull Secret 时，Pod 校验允许 ani-registry-pull 前缀的 SA 继承引用。step 镜像由 pipeline compiler configuration.component_image 固定为 digest，源码未给实际租户 SA 配置提供现场证据。

实施影响：需要分别取 step/training/fixed finetuning SA 与 Secret 元数据；Res 静态 allowlist 由 Res 负责核验；本任务不调用 Ensure 创建 Secret。

源码：
- [`internal/biz/runtime_plan.go:64–92`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/runtime_plan.go#L64-L92)
- [`internal/biz/managed_runtime.go:354–373`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/biz/managed_runtime.go#L354-L373)
- [`internal/data/imageruntime/client.go:205–227`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/imageruntime/client.go#L205-L227)
- [`internal/data/trainer/pod_contract.go:41–81`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/trainer/pod_contract.go#L41-L81)
- [`pipelines/cpu_p01.py:49–75`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/pipelines/cpu_p01.py#L49-L75)

缺口：实际 Secret 是否存在/type/ref、SA imagePullSecrets、registry 可拉取性与 Res allowlist 未读取。

## SRC-MD-15 — STS scopes 在启动复制；基础凭据可按读取轮转

检查：`K09.SOURCE.STS`；状态：**FAIL / SOURCE**。

runtimeStorageMaterial 读取 credentials_file 中 sts_bindings；loadRuntimeClients 传给 storagecredentials.New，New 校验 1..64 且复制 Bindings。scope 匹配 tenant+purpose+connection+bucket+prefix，prepare/base-model 为精确源对象读，publish 为 ApprovedPrefix/executionID/ 下读写。mountedStorageCredentials 在 SDK retrieval 时重读文件以支持基础凭据轮转；这不会替换 Issuer.config.Bindings。

实施影响：动态租户需要替换 scope 注册来源，同时保留按执行 scope 的细粒度凭据授权；不能宣称文件轮转已实现动态 scope 加载。

源码：
- [`cmd/ani-modeldev-service/runtime_clients.go:92–103`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/cmd/ani-modeldev-service/runtime_clients.go#L92-L103)
- [`cmd/ani-modeldev-service/runtime_clients.go:181–209`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/cmd/ani-modeldev-service/runtime_clients.go#L181-L209)
- [`internal/data/storagecredentials/issuer.go:30–77`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/storagecredentials/issuer.go#L30-L77)
- [`internal/data/storagecredentials/issuer.go:191–230`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/storagecredentials/issuer.go#L191-L230)

缺口：实际 endpoint、bucket、prefix、pipeline root、STS scope/role 标识与 A/B 关联均未读取；没有颁发凭据或对象读写。

## SRC-MD-16 — 旧数据仅列 ModelDev 自有记录和有原 UID 证据的受管对象

检查：`K12.SOURCE.REMOVAL_SCOPE`；状态：**PASS / SOURCE**。

固定 main 的 baseline 定义 9 张 ModelDev 表：modeldev_close_intents、modeldev_execution_cleanup_audits、modeldev_execution_identities、modeldev_execution_runtimes、modeldev_executions、modeldev_input_versions、modeldev_pipeline_confirmed_runs、modeldev_pipeline_dispatches、modeldev_run_authorities。执行表按 tenant/execution 关联 identity；run authorities 存 namespace/UID、workflow/UID；runtimes facts 存 workspace/training/close/GPU outbox 等。

实施影响：后续删除旧 binding/facts/STS/webhook/static policy 材料与这些表内限定 ModelDev 旧记录；先用原执行证据关闭/移除其自有 Run/Workflow/TrainJob/JobSet/Job/Pod/Runtime/工作区，保留共享 Namespace、其他 owner 记录、原数据/基模型/共享镜像/连接。input_versions 是 ModelDev 元数据，不能由元数据删除推导可删除外部源对象。此处不生成清空 SQL。

源码：
- [`migrations/0001_current_baseline.up.sql:67–113`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/migrations/0001_current_baseline.up.sql#L67-L113)
- [`migrations/0001_current_baseline.up.sql:130–197`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/migrations/0001_current_baseline.up.sql#L130-L197)
- [`migrations/0001_current_baseline.up.sql:267–334`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/migrations/0001_current_baseline.up.sql#L267-L334)
- [`migrations/0001_current_baseline.up.sql:508–580`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/migrations/0001_current_baseline.up.sql#L508-L580)
- [`internal/data/runtimeproof/compute_cleanup.go:94–118`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/runtimeproof/compute_cleanup.go#L94-L118)
- [`internal/data/runtimeproof/compute_cleanup.go:183–218`](https://github.com/zhangzhe-ctrl/ani-modeldev-service/blob/21bf4bd6540d462a61c91b629f26250899583223/internal/data/runtimeproof/compute_cleanup.go#L183-L218)

缺口：未查询数据库、未统计旧行、未确认现场表版本；KFP Experiment/Pipeline 定义可能共享，须由现场归属证明后才能决定删除。

## 不能由源码落定的决定

- KFP 现场模式、shared-read、入口/可信头或 SA 认证、control principal/audience 与权限，决定唯一的 TokenProvider 方案。
- Experiment/Run/Workflow 原对象关系与 Pipeline 父对象归属，决定可共享固定 Version 还是需按租户登记；SOURCE 不足以选择。
- 实际 A/B Namespace 标签、UID、SA/RBAC、controller watch、webhook/policy、网络/PSA/Quota、镜像/STS scope，决定各租户专属配置缺口。
- 实施后仍须以两个租户的真实新执行贯穿持久 admission、唯一派发、正确落点、回调/训练、停止/关闭/清理和 outbox，源码核验不覆盖这些验收。

结构化 findings、source_version 与带 UTC/真实 exit code 的取证命令：[SRC-version-metadata.json](SRC-version-metadata.json)；命令见 [commands.tsv](commands.tsv)。
