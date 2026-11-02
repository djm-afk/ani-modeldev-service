# ModelDev KFP / Tenant Namespace 核验报告

任务包版本：v1.1。指定及实际 SSH 入口：`kubercloud@100.64.128.12`；远程执行主机 `node1`，用户 `kubercloud`。本地仅做源码读取、轻量分析和文件整理；现场由已有受管运维通道读取。

核验窗口（UTC）：2026-10-31T14:39:45.979452Z 至 2026-10-31T15:38:28.060678Z；报告整理于 2026-10-31T15:43:04.202988Z。初次发现命令的失败保存在 K00-discovery-attempt1.json，后续发现成功，不隐去失败。

目标 context：`kubernetes-admin@ani-kcn-full`；API：`https://lb.kubesphere.local:6443`；平台 cluster_id：`f39bf6e2-f3ed-4191-ada3-caeebf86c220`；kube-system UID：`3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24`；Kubernetes v1.35.8。平台 ID 来自实际 Res/Gov/ModelDev 配置，不以 SSH IP 或 kube-system UID 替代。

任务边界：未创建或修改 Namespace、Profile、Experiment、PipelineVersion、Run、训练资源、SA、RBAC、Secret、Webhook、Policy、Queue；未申请新凭据、运行 ensure/初始化/部署/清理/server dry-run/重型构建。执行了 GET/LIST、有限原 Pod 只读探针、既有数据库 SELECT（强制只读事务参数）及 SubjectAccessReview。凭据仅在受控进程内使用，证据不保留 token、密码、私钥、DSN、Secret data 或训练内容。Secret 存在性用 PartialObjectMetadata，未取 data；该响应不含 type，所以 type 保持 null。

## 结论

**A/B 的标准租户 Namespace 均已存在且归属正确；共享 ModelDev 动态接入尚未完成。主链目前仍停在旧单租户环境的静态 binding、镜像/STS 授权材料及目标 Namespace 的必要权限接入。** 现有历史执行能证明旧环境的部分真实运行与控制器调和，不能证明 A/B 标准 Namespace 已接受新执行或同一进程可以双租户派发。

KFP 入口 healthz 实测为 2.16.0、multi_user=true、pipeline_store=database；Pod 显式 env 为 shared-read=false、REQUIRE_NAMESPACE_FOR_PIPELINES=true、TOKEN_REVIEW_AUDIENCE=pipelines.kubeflow.org。已核对进程 `--config=/config` 与 `/config/config.json` 七个目标键缺失，结合匹配相关上游源码，可按显式 env 解释这些键。入口 Nginx 终止 TLS、转发 Bearer、拒绝/清除列出的用户身份头。**有效 ModelDev token 通过该入口的正向 GET、TokenReview 与最终 principal 仍 BLOCKED**：原 ModelDev Pod 没有已试的 shell/python/curl/busybox，不能在不导出 token、不装工具、不建 Pod的边界内完成。无凭据与固定无效 token 都返回 401，仅说明这两次请求被拒绝；没有正向基准，不能宣布租户隔离通过。

已完成实际 API version 的配置 SA 权限询问：KFP API Server 在 A/B 创建/读取 Workflow 被拒；ModelDev control 的 Namespace GET、训练/观察/日志等已问询动作在 A/B 被拒；webhook Namespace GET 在 A/B 被拒。Argo/Trainer/JobSet 已问询动作获允许，watch 范围由 SOURCE+CONFIG 推断覆盖 A/B，但 A/B 没有现有对象可证明调和。SAR 使用配置 SA user/groups assertion，不是 token 认证成功。

确定的实施方向是：复用现有 A/B Namespace；统一 `tenant-<resource_tenant_id>` 与 `network.ani.io/*` 权威标签；缺失报错；替换单 binding、静态 facts/派发与关闭发现、准入和 STS/镜像 Namespace 授权；补目标范围的专属 SA/权限/Experiment 等材料。**不新增 Res Namespace 查询/开通 RPC，不再建 ModelDev 专用 Namespace，不设计旧 binding 兼容。** KFP 凭据获取/刷新及是否采用平台 control 身份、固定 Version 是否共享或按租户登记，仍须真实身份与父 Pipeline 归属证据后定案；本报告不猜唯一 TokenProvider。

## Q01–Q12 逐项回答

### Q01：最新源码与现场版本

远端 main 分别为 ModelDev `21bf4bd6540d462a61c91b629f26250899583223`、Gov `ddd98f4c79751ba848fa862bab6878dc7bc99bb7`、Res `23e25b33ca02890231cd273ef7246b0fc415476c`，与任务包核对基线一致；本地 HEAD 和 dirty 已分别保留，不以当前工作区代替 main。现场关键镜像如下，完整 workload/Pod UID、generation、SA、imageID 和就绪状态见 K00 证据。

| workload | 部署镜像 digest | Pod imageID |
|---|---|---|
| ani-system/ani-governance | sha256:0518f2d3616c8a088113b0e604540565d4419ae1921446d17ca4b233c4090fc0 | 与部署 digest 相符 |
| ani-system/ani-modeldev | sha256:b0358ec0c5babcdaa97c645c06f89564478406715a4958e121099c4f6534e38d | 与部署 digest 相符 |
| ani-system/ani-modeldev-webhook | sha256:aab84d298107fa61f10cf1b3d7ea6085297cb12896f07daa3eaa086c78c51e03 | 与部署 digest 相符 |
| ani-system/ani-resource-service | sha256:71c04b8948728d8d0299320f148afa3fee3eabdbea87051c2b99c300abe730d1 | 与部署 digest 相符 |
| kubeflow/ml-pipeline | sha256:5f9d674f75dbd9c056ad31a1d8c5432e1bdc1f0597dfefe6f58d00af983fbe22 | 与部署 digest 相符 |
| kubeflow/workflow-controller | sha256:5c8f5b6e5a2134dafdeadf758f230e4cc08db78f813e52d928e8f4f4541f243d | 与部署 digest 相符 |
| kubeflow-system/kubeflow-trainer-controller-manager | sha256:aa5949fc4ccbbed2537fa05f08ca330bc83b40aef930d2a0d44970922f197a73 | 与部署 digest 相符 |
| kubeflow-system/jobset-controller-manager | sha256:a018e2ca9b2a73645e108930a1a1795392fe8b765e4c81f5240f640b854436c4 | 与部署 digest 相符 |

KFP healthz commit 是 `85a38e0719870e5b2f97a74f5c966d79bcc7c960`，官方 2.16.0 tag 解析为 `e4ebca310f404dac306e16bc880de12bbbf63b95`，两者不同。七个已核对相关源码文件 byte-identical，支持本报告限定配置/认证/SAR解释；不证明整个仓库、依赖、二进制或镜像同源。ANI 三服务缺可信 build→digest→main 映射，状态 **SOURCE_IMAGE_MAPPING_NOT_VERIFIED**，不能声称现场正运行 latest main。

证据：`K00-discovery` [文件](evidence/K00-discovery.json)，2026-10-31T14:39:45.979452Z，CONFIG；`K03-K04-entry-probes` [文件](evidence/K03-K04-entry-probes.json)，14:51:53.598497Z，READ_API；`SRC-GR-00` [文件](evidence/SRC-govres.md)，14:39–14:44Z，SOURCE；`SRC-MD-01` [文件](evidence/SRC-modeldev.md)，14:39:25–14:52:04.962299Z，SOURCE；[SRC-kfp-upstream](evidence/SRC-kfp-upstream.md) 与 [AN-controller-matrix](evidence/AN-controller-matrix.json) 的 upstream_source_provenance（逐请求 UTC/哈希），SOURCE。缺口：ANI 源码与现场制品映射、完整 KFP 镜像构建来源。

### Q02：租户 UUID、前缀与规则差异

可信链为 Gov 已认证 Principal → 当前授权 → `sys_tenants.resource_tenant_id` → 重建出站 `x-ani-tenant-id`；该字段独立、唯一、不可变，resolver 拒绝零 UUID/非规范 UUID/平台 tenant 0。现有 Tenant HTTP DTO 不暴露该 UUID，本次用现有受管 PostgreSQL 的只读 SELECT 限定候选，得到 A Gov2、B Gov40 的持久映射。Res 实际挂载配置 `network.namespace_prefix=tenant-`；network.cluster_id 与 image.pull_secret_cluster_id 均为上述平台 UUID。A/B 实际标签符合规则 v1。

差异在旧 ModelDev 环境和源码：当前 ModelDev binding/webhook/镜像 caller 仍指定旧 `ani-modeldev-tenant-a-v21`，使用 `ani.io/resource-tenant-id` / `ani.io/managed-by=modeldev-execution-v21`；Res Network/Sandbox 仍有 NotFound→Create，且源码允许其他合法 prefix。现场 prefix 符合不能消除这些实现差异。

证据：`K01-K05-managed-db` [文件](evidence/K01-K05-managed-db.json)，2026-10-31T15:05:45.990748Z，READ_API（持久只读映射）；`K01-K09-res-config` [文件](evidence/K01-K09-res-config.json)，14:52:37.226285Z，CONFIG；`K01-K03-configs` [文件](evidence/K01-K03-configs.json)，14:41:54.934318Z，READ_API/CONFIG；`SRC-GR-01/02/03/04`、`SRC-MD-09`，上述源码窗口，SOURCE。缺口：本次不验证 Gov 公网登录/创建新租户与完整初始化流程。

### Q03：A/B Namespace 的存在、UID 与归属

A：`tenant-64c5c35a-f554-432c-b4bc-7b4f36f08420`，UID `a8d8a472-8a8a-43cf-abf0-0e805bbe67b0`。B：`tenant-207328fc-f08d-4dc4-b403-16ec86396ce2`，UID `8698b0b5-f4c8-4770-85e5-10f25ad7b347`。两者精确 GET 均成功，Active、无 deletionTimestamp，`network.ani.io/tenant-id` 匹配各 UUID，`network.ani.io/managed-by=ani-network-service`。**K01.A/B 在 READ_API 等级 PASS**，没有以旧名回退补证。当前旧 ModelDev Namespace UID `94f3afc0-7548-4099-9e11-3fa7fdf6cc67`，与 A 标准 Namespace 是两个实物。

证据：`K01-K03-configs` [文件](evidence/K01-K03-configs.json) namespace_exact_reads，2026-10-31T14:41:54.934318Z，READ_API；`K01-K05-managed-db`，15:05:45.990748Z，READ_API。缺口：操作者可读不等于 ModelDev SA可读；后者 SAR 在 A/B 为 false，见 Q08。

### Q04：KFP 实际模式与配置来源

healthz 的 200 JSON 精确输出 multi_user=true、pipeline_store=database；显式 env：`MULTIUSER=true`、`MULTIUSER_SHARED_READ=false`、`REQUIRE_NAMESPACE_FOR_PIPELINES=true`、`TOKEN_REVIEW_AUDIENCE=pipelines.kubeflow.org`、`KUBEFLOW_USERID_HEADER=:`、`KUBEFLOW_USERID_PREFIX=""`，`POD_NAMESPACE` 来自 downward API，值 kubeflow。envFrom 引用 ConfigMap 已追踪；进程 argv 为 `--config=/config`，原 Pod `/config/config.json` 七项目标键均缺失。对应上游初始化与配置优先级支持显式 env 优先，不能把空 prefix 或缺键写成默认 false。`:` 是字面 header 键，源码无特殊 disable 分支。

证据：`K00-discovery`，2026-10-31T14:39:45.979452Z，CONFIG；`K01-K03-configs`，14:41:54.934318Z，CONFIG；`K03-K04-entry-probes`，14:51:53.598497Z，READ_API/CONFIG；`SRC-kfp-upstream` 与 `AN-controller-matrix.upstream_source_provenance` 逐请求 UTC，SOURCE。缺口：缺真实业务身份 GET 基准，multi-user/shared-read 配置不等于隔离验收；health commit 字段也不替代二进制来源证明。

### Q05：入口、身份、audience 与回调

配置链为 ModelDev `https://ani-kfp-entry.kubeflow.svc:8443` → Nginx TLS 终止 → `http://ml-pipeline.kubeflow.svc.cluster.local:8888` → KFP。ModelDev CA 引用 `/etc/modeldev/ca/kfp-ca.crt`，来自受管 ConfigMap 投影；实际 TLS GET 校验成功，证书 SHA256 已保存。Nginx 透传 Authorization，拒绝列出的伪造用户头并将若干身份头清空；配置未显示外部用户目录代理。KFP 源码先试用户头再试 Bearer TokenReview，入口配置和 invalid-token 的 TokenReview 拒绝响应支持当前 Bearer 路径判断，但没有有效 token 的最终 principal 证据，也没有验证所有可达路径都不能绕过入口。

control 配置身份为 `system:serviceaccount:ani-system:modeldev-control`；`kfp-token` projected volume audience=`pipelines.kubeflow.org`、expirationSeconds=3600，挂载 `/var/run/modeldev-kfp/token`。这是配置身份/audience，未做有效 token TokenReview 或正向 KFP GET。原 Pod 中四种已试探针程序均不存在，状态 **BLOCKED / NO_USABLE_RUNTIME_IDENTITY_PROBE_CHANNEL**；未将 token 带出 Pod、签发新 token 或创建调试 Pod。

step→ModelDev 是另一条链：现场 `runtime.token_audience=https://kubernetes.default.svc.cluster.local`，SOURCE 显示 verifier 显式 TokenReview 并检查 Pod/SA/Workflow/Namespace 原 UID；不得与 KFP control audience 混用。本次未验证现成 step token。

证据：`K00-discovery`，2026-10-31T14:39:45.979452Z，CONFIG；`K01-K03-configs`，14:41:54.934318Z，CONFIG；`K03-K04-entry-probes`，14:51:53.598497Z，READ_API；`SRC-MD-03/04/05`（14:39:25–14:52:04.962299Z）、`SRC-kfp-upstream`（provenance逐请求 UTC），SOURCE。缺口：真实 principal/audience 验证、正向/跨租户 GET、所有可达入口的约束；因此身份选择与刷新方式未落定。

### Q06：Experiment、Run 与真实 Namespace 落点

SOURCE 的 CreateRun 发送 Experiment ID、固定 PipelineVersion、pipeline root 与 step SA，不另发 namespace；现场对应版本按 Experiment 解析 namespace，并据此创建 Workflow。当前配置 Experiment=`fb392303-5d43-45fc-af68-7100af343f8b`，binding 仍指旧 Namespace；该 Experiment 的实时 GET 没有正向成功，不能仅凭配置断言当前 Experiment.namespace。

现有只读持久权威记录与冻结快照关联了三条旧环境执行：Run `39e127f0-8b66-4748-8490-950fba5286da`→Workflow `md-jdlgv`/UID `c4797422-f328-49e6-a378-d40f28bfce8a`；Run `bd72621d-216d-43a9-80a6-aaca3e95d175`→`md-qtpb6`/UID `08a21c2c-d9f3-4869-875a-5d0248f0a95c`；Run `dbb95879-7aa3-4465-82d1-c7bdfa313f2f`→`md-ckq98`/UID `5f6d0722-a1f9-4e36-a640-4b22e373fc7e`。三者均冻结旧 Namespace UID、上述 Experiment 与当次 PipelineVersion。这是 **EXISTING_EXECUTION 历史权威事实**；当前 Workflow LIST 为零，A/B 标准 Namespace 的 Workflow/TrainJob/TrainingRuntime/JobSet/Job 也均零。

证据：`K05-K09-final-live` [文件](evidence/K05-K09-final-live.json)，2026-10-31T15:29:13.115573Z，EXISTING_EXECUTION；`K07-K09-resources` [文件](evidence/K07-K09-resources.json)，14:43:29.160255Z，READ_API；`SRC-MD-05` 与 `SRC-kfp-upstream`，上述SOURCE时间。缺口：当前精确 Experiment/Run GET、A/B 真实执行链、实时 Workflow；无新建 Run。

### Q07：固定 PipelineVersion 能否供第二租户使用

主 catalogue 列四个版本：MLP CPU `ef17d352-9fdd-46e4-b259-b2c6adc5b5c8`，MLP GPU `7ecdea6b-e05b-45b0-a5b1-b2d5eb252266`，SFT CPU `4bb2e676-64a6-419d-b46d-8a58416e76a1`，SFT GPU `f7bb501f-0e18-41a7-af2f-49d6db8c05dd`。前三条历史 authority 仅证明旧 A 执行使用过所列 MLP CPU/GPU Version。webhook 的 MLP CPU 材料另为 `c060e706-cb11-4043-a3e8-4f6cdf135178` 且 IR hash 不同，应保留材料差异，不自行择一当实际当前版本。

父 Pipeline 的 namespace/name/共享属性、B计划控制身份精确 Version GET 均 **NOT_VERIFIED**；不能认定四个定义平台共享或已为 B 授权。配置 control principal 的 runs/create SAR 按 KFP 语义省略 groups/name，旧 Namespace true、目标 A/B false；它是 **AUTH_REVIEW 配置身份询问**，不是已认证 B 的调用。`B_READS_EXACT_PIPELINE_VERSION`、`B_EXISTING_RUN_USED_EXACT_VERSION` 均缺证据；不能用 `?namespace=B` 或普通 API路径猜 PipelineVersion SAR属性。

证据：`K01-K03-configs`，2026-10-31T14:41:54.934318Z，CONFIG；`K05-K09-final-live`，15:29:13.115573Z，EXISTING_EXECUTION；最终 `K06-K07-auth-reviews` [文件](evidence/K06-K07-auth-reviews.json)，15:26:28.025116Z，AUTH_REVIEW；`SRC-kfp-upstream`，逐请求 UTC，SOURCE。缺口阻断共享定义/按租户登记的选择及最终 TokenProvider；此处明确不猜。

### Q08：controller watch、权限与已有调和

Argo 3.7.3 未配置 namespaced/managed-namespace 限制，ConfigMap 无 InstanceID；Trainer 2.1.0 与 JobSet 0.10.1 的实际配置未限制 cache namespace，结合相应版本默认源码，**WATCH_SCOPE 为 SOURCE+CONFIG 覆盖 A/B 的推断**。不能把缺 WATCH_NAMESPACE 单独当证明。

最终 66 项 SAR 均命令 exit=0、使用真实 resource API version；按配置 SA assertion：KFP API Server 的 Workflow create/get 旧 Namespace true、A/B false；Argo Workflow watch/update与Pod create/list三处均 true；Trainer TrainJob watch、TrainingRuntime get、JobSet create三处均 true；JobSet watch/Job create三处均 true；ModelDev control 的9项 Namespace/训练/观察/日志等动作旧 true、A/B false；webhook Namespace GET旧 true、A/B false。KFP runs/create另见Q07。未问询动作保持未验证，SA assertion 不替代真实token认证。

旧环境保留三条 TrainJob→JobSet→Job UID链，其中一条 Complete/Completed/succeeded=1/Pod Succeeded；44个 Workflow-owned Pod均有删除时间，38 Succeeded/6 Failed，是历史后代而非当前Workflow。A/B 无现成执行对象，**OBSERVED_RECONCILIATION.A/B=NOT_VERIFIED**。

证据：`K00-discovery` 14:39:45.979452Z、`K01-K03-configs`14:41:54.934318Z，CONFIG；`K07-K09-resources`14:43:29.160255Z，READ_API/EXISTING_EXECUTION；最终 `K06-K07-auth-reviews`15:26:28.025116Z，AUTH_REVIEW；[AN-controller-coexistence](evidence/AN-controller-coexistence.md)、[AN-controller-matrix](evidence/AN-controller-matrix.json)（analysis_at_utc=2026-10-31T15:37:18.994532+00:00；按输入UTC解释）、`SRC-kfp-upstream`，SOURCE+CONFIG/分析。缺口：A/B新对象调和与未问询动作；不扩成全量RBAC重构。

### Q09：Profile 是否实际参与

本次读取 Profile 返回 `the server doesn't have a resource type "profiles"`（exit 1）。未发现可读取的Profile资源，入口/ModelDev配置体现受管SA、Bearer与RBAC链；没有证据要求为复用 A/B Namespace 新建 Profile 或再建 Namespace。ModelDev webhook 中的 `profiles` 是本服务执行材料配置名，不应与 Kubeflow Profile CR 混淆。

证据：`K07-K09-resources` profiles，2026-10-31T14:43:29.160255Z，READ_API；`K01-K03-configs`14:41:54.934318Z，CONFIG；`AN-controller-coexistence`依据上述时间。缺口：有效控制身份正向认证尚缺，因此不把“未发现Profile类型”外推为全部认证/用户目录已完整证明；所需专属接入项是实际SA/RBAC、Experiment及运行材料，见Q10/Q12。

### Q10：共用 Namespace 的实际冲突与专属条件

A/B 只有 default/workload/ani-vm-workload SA，未见旧环境的 modeldev-step、trainer-workload、verifier。A 有5个Running Pod（VM launcher/hotplug/sandbox），12个PVC；B本次Pod/PVC为空。A的VM/CDI/Sandbox卷属于其他owner，不能按ModelDev清理。三Namespace均未观察到NetworkPolicy/ResourceQuota/LimitRange；这不证明实际网络互通或不存在平台/集群级限制。旧Namespace有restricted PSA标签，A/B未记录此类标签；无标签不能推断无集群默认PSA。

实际 ModelDev MutatingWebhookConfiguration 只选旧 namespace名与旧owner，objectSelector为空、无记录的matchConditions、所有匹配范围Pod CREATE、failurePolicy=Fail。A/B当前不匹配；若仅扩大namespaceSelector，会把普通Instance/VM Pod纳入对webhook可用性的依赖，handler可能no-op并不能消除调用前故障。需将准确工作负载匹配、统一归属与动态UID binding一起修改，保留校验，不能改Ignore制造接入。

现存固定VAP `ani-kfp-trainjob` 的binding选 kubeflow-tenant标签并排除旧namespace，A/B因无该标签也不匹配；若直接加标签，会激活固定ClusterTrainingRuntime、trainer-workload、单节点/进程、2CPU/4Gi等限制。旧成功链用namespaced TrainingRuntime，不能原样假定兼容；参数ConfigMap存在性未确认。另有ModelDev源码generator固定全局policy名，动态化须避免租户互相覆盖。源码readiness遍历所有静态binding，一个租户故障可使共享readyz失败，需拆开租户状态与实例就绪。

证据：`K07-K09-resources`，2026-10-31T14:43:29.160255Z，READ_API/CONFIG；`K01-K03-configs`14:41:54.934318Z，CONFIG；`SRC-MD-09/10/11/12/13`14:39:25–14:52:04.962299Z，SOURCE；`AN-controller-coexistence`、`AN-controller-matrix`（2026-10-31T15:37:18.994532+00:00）归并分析。缺口：网络实际连通、默认PSA行为、webhook故障影响及A/B执行PVC生命周期；本次无探测Pod/新训练。

### Q11：镜像与存储范围是否静态受限

是。Res 实际ModelDev caller只允许旧namespace、owner=modeldev-execution-v21、require_tenant_label=true，未允许A/B标准namespace；writer标签仍为ani.io两键，与network权威标签不一致。通用训练源码走Res Ensure冻结Secret ref和真实Namespace UID；固定微调模板与step镜像/SA继承是另一条引用路径，不能合并成“已可拉取”。已用PartialObjectMetadata确认旧namespace `ani-registry-pull`存在（UID `0bdb0fc9-05ef-4db2-a18c-53251e52a5d4`），A有Sandbox自有pull Secret（UID `459e44ce-41e7-4722-85f9-2b9f51152afc`），两者type未返回且不证明ModelDev在A/B可用。两个人工候选名404仅证明那两个名字不存在，不能外推全部Secret缺失。

对象存储endpoint配置为 `https://ani-rustfs-svc.ani-platform.svc.cluster.local:9000`，connection=`modeldev-newenv-rustfs-20261022`；pipeline root=`s3://ani-md-v21-kfp-01a15f47/artifacts`。实际STS仅有A三条scope：prepare bucket `ani-md-v21-input-01a15f47` / prefix `inputs/fixed`；prepare-base-model bucket `ani-models-v21-01a15f47` / prefix A UUID；publish bucket `ani-md-v21-output-01a15f47` / prefix `executions/<A UUID>/modeldev-v21-01a15f47`。没有B scope。SOURCE显示scope在启动复制，重读基础凭据并不动态重载Bindings。凭据来自现有storage-credentials.json Secret挂载，未导出值/颁发STS/访问业务对象。

证据：`K01-K09-res-config`14:52:37.226285Z，CONFIG；`K01-K03-configs`14:41:54.934318Z，CONFIG；`K05-K09-final-live`15:29:13.115573Z，CONFIG/READ_API；[K09-secret-metadata](evidence/K09-secret-metadata.json)15:38:28.044820Z，READ_API；`SRC-GR-04`与`SRC-MD-14/15`，各SOURCE窗口。缺口：A/B ModelDev最终Secret ref/type、node侧镜像拉取、B存储范围与真实读写/更新行为；无新凭据或对象存储写入。

### Q12：最小实施范围与仍未落定的决定

已能确定实施边界：单共享ModelDev进程；复用标准租户Namespace并精确GET归属/UID/删除状态；整体替换旧单租户逻辑与材料；同次覆盖提交、派发、回调、训练、停止、关闭、workspace清理及GPU release outbox。下节按owner列必要改动。无需等待认证缺口重议“不建第二套Namespace、不新增Namespace RPC、不兼容旧binding”这些已定边界。

仍不能选定具体TokenProvider/平台control或租户身份授权方式，也不能决定固定Pipeline全局共享或租户注册：缺有效身份正向GET/TokenReview及父Pipeline归属。当前权限拒绝、材料差异与无B scope已足以要求对应接入改造；不会被旧环境成功记录抵消。实施后必须以A/B各一条真实新执行贯穿主链，再验证跨租户拒绝、UID重建拒绝、幂等唯一派发、停止关闭和清理边界；本次未覆盖。

证据：`SRC-GR-06`、`SRC-MD-02/03/06/07/08/09/10/11/12/14/15/16`，各SOURCE窗口；最终`K06-K07-auth-reviews`15:26:28.025116Z，AUTH_REVIEW；`K05-K09-final-live`15:29:13.115573Z，CONFIG/EXISTING_EXECUTION；`AN-controller-coexistence`（2026-10-31T15:37:18.994532+00:00）分析。缺口、后续实施验收分别列于末节。

## K00–K09 事实表

| 检查 | 已确认的结果 | 状态/等级 | 尚缺与实施影响 |
|---|---|---|---|
| K00 | 组件、digest、context/cluster指纹已发现，main已固定 | PASS CONFIG/SOURCE；源码镜像映射NOT_VERIFIED | 不宣称现场等于main |
| K01 | A/B标准名字/UID/权威标签正确、prefix=tenant- | PASS READ_API/CONFIG；统一源码仍FAIL SOURCE | 去除Network/Sandbox自动创建，统一标签/UID |
| K02 | 当前单binding指旧Namespace，固定facts/身份引用已读 | PASS CONFIG；动态接入FAIL SOURCE | 替换单binding与后台发现 |
| K03 | health/env/file/入口链相互支持multi-user、shared-read=false | PASS READ_API/CONFIG/SOURCE | 完整镜像来源/所有可达入口未证 |
| K04 | 无凭据/无效token401；control token探针通道缺失 | BLOCKED 正向认证；负向READ_API已完成 | 不宣布隔离或实际principal已验证 |
| K05 | 3条旧Run权威与冻结快照/WorkflowUID关联 | PASS EXISTING_EXECUTION；A/B NOT_VERIFIED | 当前Workflow=0，未新建Run |
| K06 | 固定定义材料和历史MLP版本已定位；A/B runs/create配置principal被拒 | AUTH_REVIEW拒绝；B读/已有Run NOT_VERIFIED | 父Pipeline归属和真实身份缺证 |
| K07 | 三controller scope SOURCE+CONFIG覆盖，已问询控制动作允许；KFP/MD目标权限缺口确认 | PASS配置询问；目标reconcile NOT_VERIFIED | 定点补KFP/MD/webhook权限 |
| K08 | 旧webhook/VAP选择器、A的他owner对象和目标缺SA已确认 | PASS CONFIG/READ_API；接入尚未实施 | 收窄准入，保留普通工作负载与清理边界 |
| K09 | Res旧namespace静态名单、A-only STS、部分Secret元数据已确认 | 动态范围FAIL CONFIG/SOURCE；拉取/读写NOT_VERIFIED | 动态scope及受信namespace授权 |

各行对应Q项中的具名文件、UTC和具体证据等级；不另设总READY状态。`PASS`仅对该项声明等级成立。

## 租户环境映射

| 样本 | Gov ID / resource_tenant_id | 标准 Namespace / UID | 归属 | Experiment / 身份材料 |
|---|---|---|---|---|
| A | 2 / `64c5c35a-f554-432c-b4bc-7b4f36f08420` | `tenant-64c5c35a-f554-432c-b4bc-7b4f36f08420` / `a8d8a472-8a8a-43cf-abf0-0e805bbe67b0` | network两键正确，Active | 标准namespace的Experiment未取得；缺旧step/trainer/verifier SA |
| B | 40 / `207328fc-f08d-4dc4-b403-16ec86396ce2` | `tenant-207328fc-f08d-4dc4-b403-16ec86396ce2` / `8698b0b5-f4c8-4770-85e5-10f25ad7b347` | 同上 | 无B执行/Experiment/STS已配置证据；独立B身份未验证 |
| 旧A环境 | 同A UUID | `ani-modeldev-tenant-a-v21` / `94f3afc0-7548-4099-9e11-3fa7fdf6cc67` | 旧ani.io两键 | 配置/历史快照Experiment `fb392303-5d43-45fc-af68-7100af343f8b`；control=`ani-system:modeldev-control`、step=`modeldev-step`、training=`trainer-workload`、verifier=`verifier` |

三行目标集群配置均为 `f39bf6e2-f3ed-4191-ada3-caeebf86c220`；旧环境不是A标准Namespace的可接受回退。

## 认证与授权请求矩阵

| 检查 | 声明角色/身份 | 精确目标 | 结果/对照 | 可得结论 |
|---|---|---|---|---|
| K03.health | anonymous | 入口v2beta1 healthz | 200 JSON，TLS验证 | 健康/模式字段，不证明业务授权 |
| K04.no-token | 无凭据 | 配置中的私有Experiment ID | 401，无有效正向基准 | 此请求拒绝，隔离NOT_VERIFIED |
| K04.invalid-token | 固定无效token | 同一Experiment | 401，响应指TokenReview失败 | 无效token拒绝，隔离NOT_VERIFIED |
| K04.control-positive | 配置control SA，未token验证 | 同一Experiment/TokenReview | BLOCKED；原Pod无可用探针程序 | principal/audience实际验证缺证 |
| K04.A/B私有对象矩阵 | 独立A/B身份 | A/B已知对象 | NOT_VERIFIED，无可用身份/完整对象基准 | 不用admin或平台身份冒充A/B |
| K06.runs-create | configured control user，无groups | 旧/A/B namespace，runs/create | true/false/false | 仅配置principal SAR权限，不是认证或CreateRun |
| K07.KFP-workflow | configured ml-pipeline SA assertion | 旧/A/B Workflow create/get | true/false/false | 目标缺KFP创建权限 |
| K07.ModelDev/webhook | configured SA assertion | 已问询的Namespace/训练/日志动作 | 旧允许，A/B拒绝 | 具体目标权限缺口 |

请求HTTP细节/UTC见K03-K04-entry-probes；最终review user/groups/真实resourceAttributes/status/逐项UTC见K06-K07-auth-reviews。initial review文件保留作过程证据，以最终实际API版本矩阵为准。

## 按 owner 的最小实施范围

| Owner | 必须改动 | 保留的现有正确性约束 |
|---|---|---|
| Gov | 复用可信Principal/授权/持久UUID链；接纳并保留环境查询明确错误；不增加Namespace选择输入或RPC | 当前授权、幂等/配额与tenant映射，不能信任浏览器tenant/cluster/name |
| ModelDev | 以受信tenant+cluster取得既存Namespace并校验UID/归属/删除状态，记录新环境/Experiment/身份快照；替换单binding、facts静态范围、mounted token整环境相等限制；派发与close/cleanup/outbox发现同步覆盖接入租户；动态STS与准入配置/命名/readiness | 沿用Reserve唯一发送许可、关闭fence、deadline、发送不确定性处理、原Namespace/Pod/SA/Workflow/运行与workspace UID验证 |
| Res | 固定tenant-规则，Network/Sandbox缺失只报错；统一Namespace校验为network两键，补应有UID校验；ModelDev镜像授权按受信UUID推导标准Namespace，保留caller/cluster限制与UID校验，替换逐Namespace静态名单 | 不补标签/收养/旧名回退；Secret自身image归属与共享生命周期不混为Namespace所有权；不新增Namespace RPC |
| 集群/运维 | 在已有A/B Namespace补ModelDev专属step/training/verifier身份与定点权限、Experiment/运行材料、必要镜像引用/存储scope；补已确认的KFP Workflow与ModelDev/webhook权限；收窄准入匹配、修复固定策略与runtime/资源路径冲突 | 不再建ModelDev Namespace，不覆盖VM/Instance网络/资源限制，不把failurePolicy改Ignore，不先加宽全局权限 |

认证实现待决：在已有受管身份通道可正向验证后，选择平台control或租户身份、明确audience和获取/刷新机制；当前配置提供SA/Bearer方向线索，但不能宣称唯一TokenProvider已确定。Pipeline定义策略待决：用真实父对象归属、精确Version GET及B权限决定共享固定定义或按租户登记。两项未决不妨碍执行已确定的Namespace/静态binding/owner边界替换。

## 后续旧逻辑与数据删除范围（本轮未执行）

直接移除旧单binding/facts/STS scopes、旧Namespace回退、单tenant/environment后台发现及旧准入静态配置，不设计导入、双读双写或老worker继续服务。清理ModelDev自有旧记录涉及9表：`modeldev_close_intents`、`modeldev_execution_cleanup_audits`、`modeldev_execution_identities`、`modeldev_execution_runtimes`、`modeldev_executions`、`modeldev_input_versions`、`modeldev_pipeline_confirmed_runs`、`modeldev_pipeline_dispatches`、`modeldev_run_authorities`；按tenant/execution及实际外键顺序限定，先保存恢复/审查必要的原UID关系。表定义SOURCE已核对，现场只读列信息不是全库行数/全表版本验收，不生成全库清空命令。

受管对象仅限有原执行/owner/UID证据的Run/Workflow/TrainJob/TrainingRuntime/JobSet/Job/Pod、execution专属workspace PVC与本服务专属绑定/策略/身份材料。共享Experiment/Pipeline/Version是否可删须先核清归属和引用。**不删除A/B共享租户Namespace、其他owner的VM/Instance/Sandbox/PVC、外部输入/基模型/共享镜像、连接或共享Res pull Secret。** input_versions是ModelDev元数据，删除它不能推导外部对象也可删。旧专用Namespace本身也不由此报告授权整Namespace删除；资源必须逐对象核清。

依据：SRC-MD-13/16、SRC-GR-04、K05-K09-final-live及AN-controller-matrix的既有UID链和PVC归属。

## 未验证项与实施后最小验收

1. **仍缺现场事实**：有效ModelDev身份的TokenReview/正向KFP GET、最终principal/audience/认证入口覆盖；父Pipeline.namespace/name/共享属性、B精确Version可读性；ANI源码→制品映射；网络实际连通/默认PSA与最终Secret type/ref等。缺口分别阻断唯一凭据实现、固定定义策略和相应运行判定，不能以401、空列表或SAR替代。
2. **前置尚未具备**：A/B专属SA/Experiment/ModelDev scopes和已确认的目标权限、准入精确匹配、统一Namespace/镜像授权；这些是现有Namespace内的接入条件，不是新Namespace理由。Namespace当前存在；未来缺失一律明确报未就绪，不自动创建。
3. **仅实施后新执行可证明**：同一共享ModelDev进程A/B各一条真实新执行，从Gov授权与持久admission到唯一派发、正确Experiment/namespace/UID、step回调、workspace/训练、停止、关闭/清理、GPU release outbox；补独立租户正向与跨租户拒绝、Namespace同名重建UID拒绝、幂等不重复派发、普通VM/Instance不受准入故障连带影响和不误删其对象。本轮未创建Run/训练，均NOT_VERIFIED。

## 证据索引

证据ID在本报告中使用文件stem（如K00-discovery、K06-K07-auth-reviews）或SOURCE笔记内编号（SRC-GR-01、SRC-MD-02）。全部必要对象UID/resourceVersion、原始响应白名单、命令与UTC详见 [evidence/evidence-index.tsv](evidence/evidence-index.tsv)、[evidence/commands.tsv](evidence/commands.tsv) 和各具名证据。证据归档仅包含evidence/；work/脚本与草稿不进入归档。SOURCE笔记中的“现场未读取”是该源码子任务自身边界，现场结论以本报告引用的另行采集文件为准。
