# Controller 接入与既有租户共存：已有证据分析

只读分析 [K00-discovery.json](K00-discovery.json)、[K01-K03-configs.json](K01-K03-configs.json)、[K07-K09-resources.json](K07-K09-resources.json)，未重新连接集群。采集 UTC 分别为 2026-10-31T14:39:45.979452Z, 2026-10-31T14:41:54.934318Z, 2026-10-31T14:43:29.160255Z。对象 UID、输入哈希和 18 行 controller × namespace 分项见 [AN-controller-matrix.json](AN-controller-matrix.json)。上游默认解释复用 [SRC-kfp-upstream.md](SRC-kfp-upstream.md)，不把 CONFIG / SOURCE 写成业务验收。

## 先确认实际断点

A 的目标是现有 `tenant-64c5c35a-f554-432c-b4bc-7b4f36f08420`，UID `a8d8a472-8a8a-43cf-abf0-0e805bbe67b0`。当前记录的 ModelDev dispatch binding 与 webhook binding 仍为旧 `ani-modeldev-tenant-a-v21`，UID `94f3afc0-7548-4099-9e11-3fa7fdf6cc67`。相同租户 ID 不使两者成为同一执行环境，旧执行成功不能证明目标 namespace 已接入。

持久 Gov 映射已确认：Gov2 → `64c5c35a-f554-432c-b4bc-7b4f36f08420`（A），Gov40 → `207328fc-f08d-4dc4-b403-16ec86396ce2`（B）。B 目标 namespace 为 `tenant-207328fc-f08d-4dc4-b403-16ec86396ce2`，UID `8698b0b5-f4c8-4770-85e5-10f25ad7b347`。依据 [K01-K05-managed-db.json](K01-K05-managed-db.json) 的只读 SQL 持久映射结果，proof_level=EXISTING_EXECUTION；这不证明独立 B token 已认证。

A/B 均只有 `default`、`workload`、`ani-vm-workload` SA；没有旧环境所用 `modeldev-step` / `trainer-workload` / `verifier`。A/B 的 Workflow、TrainJob、TrainingRuntime、JobSet、Job 均为零；历史训练 UID 链全部在旧 namespace。

## K07：范围、权限、既有处理分别判断

下表 scope 是记录的 Pod 参数/环境、引用 ConfigMap 与匹配版本源码共同支持的配置推断；ConfigMap 引用及 subPath 挂载不证明某个进程已重新加载其当前内容。RBAC 已按最终 [K06-K07-auth-reviews.json](K06-K07-auth-reviews.json) 更新为 AUTH_REVIEW，逐项属性和 allowed 布尔值见归档内矩阵。review 是 configured SA user/groups assertion（身份标记 CONFIGURED_SA_ONLY_NOT_TOKEN_VERIFIED），不含 token，不能当作认证成功；KFP runs/create 另按其源码语义省略 groups。66 项均 exit=0，版本为实际 v1 / v1alpha1 / v1alpha2 / v1beta1，没有使用 version="*"。

| 组件 / principal | 旧 namespace WATCH_SCOPE / 已有证据 | A 目标 WATCH_SCOPE / 已有证据 | B 目标 WATCH_SCOPE / 已有证据 | RBAC 配置证据 |
|---|---|---|---|---|
| KFP API / kubeflow:ml-pipeline | 请求 namespace 由 Experiment 决定；ModelDev 绑定指旧环境；本三份快照无 API 创建证据 | 当前 ModelDev 绑定不指此处；无执行对象 | 当前 binding 不指此处；无现有执行 | AUTH_REVIEW：Workflow create/get 旧 ns 2/2 true，A/B 各 2/2 false |
| Argo / kubeflow:argo | 默认全 ns 配置推断；44 个历史 Workflow-owned Pod | 默认覆盖配置推断；无现有执行 | 默认覆盖配置推断；无现有执行 | AUTH_REVIEW：每 ns 的 Workflow watch/update、Pod create/list 均 4/4 true |
| Trainer / kubeflow-system:kubeflow-trainer-controller-manager | 默认 NamespaceAll 配置推断；3 条 TrainJob→JobSet UID 链 | 同默认范围；无现有对象 | 同默认范围；无现有对象 | AUTH_REVIEW：每 ns TrainJob watch、TrainingRuntime get、JobSet create 均 3/3 true |
| JobSet / kubeflow-system:jobset-controller-manager | 默认 NamespaceAll 配置推断；3 条 JobSet→Job UID 链，1 条完成 | 同默认范围；无现有对象 | 同默认范围；无现有对象 | AUTH_REVIEW：每 ns JobSet watch、Job create 均 2/2 true |
| ModelDev control / ani-system:modeldev-control | dispatch binding 为旧 namespace/UID；不能仅凭现存对象认定本次请求走到此处 | 无对应 binding；无执行对象 | 无对应 binding；无执行对象 | AUTH_REVIEW：旧 ns 10/10 true；A/B 各 10/10 false（含 Namespace get 和 KFP runs/create） |
| ModelDev webhook / ani-system:ani-modeldev-webhook | 注册 selector 与内部 binding 都只匹配旧 ns/UID；无实际 admission 日志证据 | selector 不匹配，内部无 binding | selector 不匹配，内部无 binding | AUTH_REVIEW：Namespace get 旧 ns true，A/B false；未 review 其他 webhook GET，不能外推 |

Argo Pod 参数仅 configmap/executor-image，无 namespaced/managed-namespace flag；env/envFrom 未记录 ARGO scope override。引用 ConfigMap 只有 executor 配置，没有 InstanceID。Trainer/JobSet 的 `--config=/controller_manager_config.yaml` 引用配置均没有 namespace cache 限制。由已查版本默认值解释为全集群配置范围，不能替代 RBAC 和目标对象调和。

旧 namespace 的 44 个 Workflow-owned Pods 全带 deletionTimestamp，phase 为 38 Succeeded / 6 Failed；当前 Workflow LIST 为零。这是历史后代证据，不是当前 Workflow 或本次 Argo 调和成功。例：Pod `ft-j4ncq-1277631073` UID `d74344c2-f010-4e0d-a4e3-b9daf858c2f9` 的 controller ownerRef 为 Workflow `ft-j4ncq` UID `1e9521b7-fc75-41f2-a406-690a7c142b73`。

完整既有成功链：

- TrainingRuntime `mdrt-94f0c54c431e976392ab4421149f55d35e84e0a74346419a`，UID `1c4df396-f67c-447d-bcba-058173354090`。
- TrainJob `md-abe39ad8-c017-416b-8041-6f097e8fd50a`，UID `b600cb42-c60d-4113-853b-c147bf21e77c`。
- JobSet `md-abe39ad8-c017-416b-8041-6f097e8fd50a`，UID `a634967e-a3db-4b4f-84a8-468b22746d1b`。
- Job `md-abe39ad8-c017-416b-8041-6f097e8fd50a-node-0`，UID `63927d67-ad3f-4270-9e74-95988a2a48da`。
- Pod `md-abe39ad8-c017-416b-8041-6f097e8fd50a-node-0-0-kk7lh`，UID `bf43ad5a-5452-4307-9732-c7b6aff7fb7e`。

JobSet ownerRef 的 TrainJob UID、Job ownerRef 的 JobSet UID、Pod ownerRef 的 Job UID 均精确相等；TrainJob Complete=True、JobSet terminalState=Completed、Job succeeded=1、Pod Succeeded。其余两条同类 UID 链为 Suspended。它们证明旧环境保留既有调和结果，不证明 A/B 新建、超出已 review 动作的权限或当前部署二进制的处理身份；没有重放训练。

## K08：准入与共存的具体差异

ModelDev MutatingWebhookConfiguration 的 namespaceSelector 同时要求旧 namespace 名与 `ani.io/managed-by=modeldev-execution-v21`；objectSelector={}，记录中无 matchConditions，规则为所有该范围内 namespaced Pod CREATE，failurePolicy=Fail。内部 config bindings 也只有旧 namespace UID。直接扩大 namespaceSelector 会让目标 namespace 的普通 Pod CREATE 也进入该失败关闭 webhook；是否 mutate/reject/no-op 仍取决于业务实现。应同时处理精确工作负载匹配与新的 namespace UID binding，不能只贴租户 label。[K07-K09-resources.json](K07-K09-resources.json) 的 webhooks 与 [K01-K03-configs.json](K01-K03-configs.json) 的 modeldev-trainimg-webhook ConfigMap 是两层独立证据。

Trainer validators 对 TrainJob/TrainingRuntime/ClusterTrainingRuntime 使用 namespaceSelector={}、objectSelector={}、failurePolicy=Fail；JobSet 的 JobSet mutator/validator 同样不限 namespace，Pod mutator/validator 额外要求 `jobset.sigs.k8s.io/jobset-name` label 存在。上述记录均没有 matchConditions。普通 Instance/VM Pod 若无该 JobSet label，不匹配 JobSet Pod webhook 的对象筛选；Trainer validator 不以 Pod 为规则资源。

固定 VAP `ani-kfp-trainjob` 的 binding 要求 `ani.io/kubeflow-tenant=true` 且 namespace 名不为旧 namespace。因此旧 ns 因显式排除不匹配，A/B 因没有该 label 也不匹配。若将该 label 加给 A/B，会启用 Deny、parameterNotFoundAction=Deny 的固定策略：仅允许所配置 ClusterTrainingRuntime、单节点/单进程、CPU/内存资源（请求/上限最多 2 CPU / 4Gi），固定 trainer-workload 与 execution PVC 名称等。旧成功链使用 namespaced TrainingRuntime，不能原样假定与该策略兼容；选定 ConfigMap `ani-workspace-policy` 是否存在不在本次所读 ConfigMap 证据范围内，不能写成已存在/缺失。

| 项目 | 旧 namespace | A 目标 | B 目标 |
|---|---|---|---|
| namespace 管理标签 | modeldev-execution-v21、kubeflow-tenant=true | ani-network-service / network tenant-id | 同 A 的 Network 管理方式 |
| PSA namespace 标签 | enforce/audit/warn=restricted，enforce-version=v1.35 | 未记录 PSA label | 未记录 PSA label |
| NetworkPolicy / ResourceQuota / LimitRange | 0 / 0 / 0 | 0 / 0 / 0 | 0 / 0 / 0 |
| PVC | 46，均无 ownerReferences | 12；3 无 ownerRef，2 VirtualMachine owner，7 DataVolume owner | 0 |
| 当前其他工作负载 | 既有训练后代 | 5 Running Pod：3 VM launcher、1 hotplug、1 sandbox | 本次 Pod LIST 为 0 |

空 NetworkPolicy 列表只说明未观察到该 API 类型的 namespace 策略；不证明实际网络互通、集群网络控制或平台配额不存在。A/B 无 PSA label 不等于 privileged 或无集群默认限制；不能把旧 namespace 的 restricted 标签直接复制作为共存已验证结论。

A 已有 PVC 属于 Resource/VM/CDI 所有权链，完整 name/UID/owner UID 保存在 [AN-controller-matrix.json](AN-controller-matrix.json)。例如 VM restore PVC `restore-70733ddb-dce5-4c91-b387-051116e3ec87-disk-3777ccb7b5965ecf9bd88e3cfc8158c8` UID `734bf717-1538-4f44-92e6-a754c4a6c922` 的 VirtualMachine owner UID 为 `381da33f-ad0e-4daf-99ae-4cec69fff582`；sandbox workspace PVC UID `ba6d1829-e610-4c3e-a0ab-1d050fac1f8e` 无 ownerRef，但带 ani-resource-service / sandbox-owner 标签。旧环境 46 个 PVC 无 ownerRef 也不能单独判作泄漏；本分析不对清理生命周期作无证据推断。接入所需执行 PVC 必须按执行 UID/所有权定点处理，不能覆盖现有 VM/Instance 卷或扩大清理范围。

当前 Profile 读取返回 resource type 不存在（exit 1），仅说明这次 API 发现没有该类型，不由此推断全部租户接入必须新建 Profile。

必要差异集中为：目标 namespace/UID 的 binding、实际所用 step/training SA 与已由 AUTH_REVIEW 指出的目标范围权限缺口、兼容普通 Pod 的精确 webhook 匹配及 binding、固定 TrainJob 策略与实际 runtime/资源路径的兼容条件。三份快照没有 A/B 执行对象，OBSERVED_RECONCILIATION 仍须保持 NOT_VERIFIED。


## 最终 AUTH_REVIEW 差异（限定已问询的动作）

| 配置身份 / 检查 | 旧 namespace | A：Gov2 目标 | B：Gov40 目标 |
|---|---|---|---|
| KFP API SA：Workflow create/get | allowed=true / true | false / false | false / false |
| Argo SA：Workflow watch/update，Pod create/list | 4 项 true | 4 项 true | 4 项 true |
| Trainer SA：TrainJob watch，TrainingRuntime get，JobSet create | 3 项 true | 3 项 true | 3 项 true |
| JobSet SA：JobSet watch，Job create | 2 项 true | 2 项 true | 2 项 true |
| ModelDev control SA：TrainJob create/get/delete、TrainingRuntime create、Workflow get、PVC get/list、Pod log get、Namespace get | 9 项 true | 9 项 false | 9 项 false |
| ModelDev control 配置 principal：KFP runs/create（无 groups） | true | false | false |
| ModelDev webhook SA：Namespace get | true | false | false |

这些结果将目标 A/B 的当前断点具体定位为 KFP API Server 创建 Workflow、ModelDev control 的训练/观察/Namespace 权限，以及 webhook Namespace get 权限；Argo/Trainer/JobSet 已问询的控制动作获允许。未问询的其他资源动作继续保留未验证，A/B 也没有既有执行调和证据。身份认证、实际新 Run、落点和训练成功仍未由 SAR 证明。原始 review 的 user、groups、resourceAttributes、status 与时间均保存在归档内 [K06-K07-auth-reviews.json](K06-K07-auth-reviews.json)；分析矩阵为 [AN-controller-matrix.json](AN-controller-matrix.json)。
