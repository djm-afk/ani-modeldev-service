# KFP 2.16：ModelDev 环境最小逻辑权限与 API server Workflow 权限

基线：现场 health SHA 85a38e0719870e5b2f97a74f5c966d79bcc7c960。以下按该提交实际 SAR 构造核对，不根据 HTTP 方法猜 RBAC verb。只读源码，未连接集群、未修改业务代码。

## 1. 结论

ModelDev control 的逻辑权限只有三类：

| 绑定范围 | SAR resource | verbs | 用途 |
|---|---|---|---|
| 已登记 tenant namespace | experiments | create, get, list | Ensure 的创建、精确查重、原 ID 核验与未知响应恢复 |
| 已登记 tenant namespace | runs | create, get, list, terminate, delete | 唯一 Run 发送、原 Run 校验、未知发送只读发现、终止及明确授权删除 |
| ani-system | pipelines | get | 固定父 Pipeline 与固定 PipelineVersion 读取；Version GET 也检查父 pipelines |

SAR 的 group 是 pipelines.kubeflow.org，version 是 v1beta1，即使 REST 路径为 v2beta1。普通 Kubernetes RBAC rule 没有 version 字段，不在 YAML 添加不存在字段。

没有 pipelineversions / runs/terminate 资源；terminate 是 runs 资源的独立 verb。不要用 update 或 patch 代替它。所需错误恢复不使用 retry、archive、unarchive、reportMetrics、readArtifact、pipelines list/create/delete，故本角色不授这些权限。平台材料注册由部署发布身份执行，不扩大 runtime control 权限。

## 2. 精确 SAR 身份与 name

该 SHA resource/resource_manager.go:1947–1955 创建 SubjectAccessReview，Spec.User 为认证器返回的完整用户名，未复制 Groups/Extra。当前实际平台身份是 system:serviceaccount:ani-system:modeldev-control。使用指向该 SA 的 RoleBinding；不要只给一个依赖 groups 的 RoleBinding 后拿普通 --as 结果冒充真实 KFP SAR。

| 操作 | Namespace | Name | 源码 |
|---|---|---|---|
| Experiment Create | 请求中的已验证 tenant ns | experiment.Name（本设计 md-env-<environment_id>） | experiment_server.go:98–110 |
| Experiment List | 请求 namespace | 空 | experiment_server.go:215–229 |
| Experiment Get | 服务端查原 Experiment 后取其 Namespace | experiment.Name，不是 Experiment UUID | experiment_server.go:169–174、341–363 |
| Run Create | GetValidExperimentNamespacePair 解析后的 Run/Experiment ns | 空 | run_server.go:137–159、638–668 |
| Run List | 请求 namespace；提供 Experiment ID 时由其 namespace 重新确定 | 空 | run_server.go:216–245 |
| Run Get/Terminate/Delete | 原 Run.Namespace；原记录为空时才回父 Experiment namespace | run.K8SName（Workflow 名），不是 run UUID、display_name 或 Experiment ID | run_server.go:187–196、343–348、465–470、638–668 |
| Pipeline Get | 原父 Pipeline.Namespace | pipeline.Name | pipeline_server.go:286–298、1080–1102 |
| PipelineVersion Get | 先从 Version 取 PipelineId，再取父 Pipeline.Namespace | 父 pipeline.Name，不是 Version 名或任一 UUID | pipeline_server.go:821–830、1061–1102 |

common/const.go:19–24 明确 group/version/resources；31–43 明确 verbs，其中第40行 RbacResourceVerbTerminate = "terminate"。

新发布工具必须显式填 v2 Pipeline.name 为总稿四个固定父名称。api_converter.go:157–189 中 Name 和 DisplayName 是两个字段；仅在 Name 为空时才使用 DisplayName。resourceNames 使用以下已固定 Name，不使用 ID 或未核对的 display_name。

## 3. 最小固定 YAML

tenant RoleBinding 由 Ensure 使用批准模板在精确标准 namespace 创建；平台材料读取 RoleBinding 由部署阶段一次安装。不要创建覆盖全部 namespace 的 ClusterRoleBinding 来替代 tenant 登记授权。

以下两个 ClusterRole 的 namespace 范围由对应 RoleBinding 确定。平台读取单独放一角色，避免把 tenant 的 Run/Experiment 权限也绑定到 ani-system。平台 read 角色由部署绑定，不加入 ModelDev runtime 的动态 bind 白名单。

~~~yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: modeldev-kfp-access-v1
rules:
  - apiGroups: ["pipelines.kubeflow.org"]
    resources: ["experiments"]
    verbs: ["create", "get", "list"]
  - apiGroups: ["pipelines.kubeflow.org"]
    resources: ["runs"]
    verbs: ["create", "get", "list", "terminate", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: modeldev-kfp-platform-read-v1
rules:
  - apiGroups: ["pipelines.kubeflow.org"]
    resources: ["pipelines"]
    resourceNames:
      - modeldev-mlp-cpu
      - modeldev-mlp-gpu
      - modeldev-sft-lora-cpu
      - modeldev-sft-lora-gpu
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: modeldev-kfp-access
  namespace: tenant-<canonical-tenant-uuid>
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: modeldev-kfp-access-v1
subjects:
  - kind: ServiceAccount
    name: modeldev-control
    namespace: ani-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: modeldev-kfp-platform-read
  namespace: ani-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: modeldev-kfp-platform-read-v1
subjects:
  - kind: ServiceAccount
    name: modeldev-control
    namespace: ani-system
~~~

这段只表达权限和身份；tenant 对象仍须加总稿指定的 environment/tenant/原 Namespace UID 所有权标记，并由 bootstrap VAP 强制固定 roleRef/subjects。动态 tenant UUID 是唯一模板变量；不能接受 RPC 提供 roleRef/subject。

不为 runs create/list 配 resourceNames，因为这两个实际 SAR 的 name 为空。全局有限 ClusterRole 也不能按未来随机 environment_id 枚举每个 Experiment 名，因此 Experiment 的作用域由 tenant RoleBinding、服务端固定对象合同和精确原记录检查共同保证。

## 4. KFP API server 的实际 Kubernetes 调用

这与上节逻辑 SAR 是两次不同授权。上节的主体是 ModelDev control；此节请求真正发往 Kubernetes，主体是部署中的 kubeflow/ml-pipeline。

针对本期选定的非 recurring Run API，最小 Workflow 集合：

| KFP API 路径 | 该 SHA 实际调用 | Kubernetes 资源 / verb |
|---|---|---|
| CreateRun | resource_manager.go:578–600 设置原 namespace 后 Execution.Create | argoproj.io/workflows create |
| TerminateRun | resource_manager.go:870–909，MergePatchType + wfClient.Patch | argoproj.io/workflows patch |
| DeleteRun | resource_manager.go:777–801，Execution.Delete | argoproj.io/workflows delete |
| GetRun/ListRuns | resource_manager.go:718–732，读取 runStore | 这些路径自身不要求 workflows get/list |
| 最终状态回报持久化标签 | resource_manager.go:1470–1472、1503–1518 | workflows patch，已在上述集合 |

因此本期租户追加 role 为：

~~~yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: modeldev-kfp-api-v1
rules:
  - apiGroups: ["argoproj.io"]
    resources: ["workflows"]
    verbs: ["create", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: modeldev-kfp-api
  namespace: tenant-<canonical-tenant-uuid>
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: modeldev-kfp-api-v1
subjects:
  - kind: ServiceAccount
    name: ml-pipeline
    namespace: kubeflow
~~~

不要为本角色加入不走的 RetryRun 所需 workflows get/update/create，或日志/重试路径的 pods/log、pods delete。相应源码：resource_manager.go:917–974 为 RetryRun，1011起为日志。选定API不调用这些路线。

该最小增量不用于删改现有KFP平台角色；KFP还为其他产品提供功能时保留其既有授权。Argo controller、persistence agent、step SA 的 list/watch/status/Pod 等权限属于各自实际调用，必须按主体分别核对，不复制到 ml-pipeline 或 ModelDev control。KFP API server 自身仍需已有 cluster-scoped TokenReview/SubjectAccessReview create 以完成鉴权；这不是授予 ModelDev逻辑资源任何额外 verb，也不是给平台 control 签发token的授权。

## 5. 错误恢复与验收不能误判

1. Experiment POST未知：原name/namespace完整List + 精确Get；只有已确认唯一约束和完整0结果才按固定body有限重发。无需授 experiments delete/archive。
2. Run未知发送：ListRuns + GetRun只发现原候选；不授 retry，不把查无结果当第二次Create许可。
3. Terminate重复原请求仍用 runs terminate；KFP内部对同Workflow merge PATCH有有限重试。成功响应不等于writer已经消失。
4. DeleteRun有额外风险：resource_manager.go:793–801 即使Workflow删除失败也会记日志并继续删除Run数据库记录。因此KFP Delete 2xx或随后GetRun 404均不能证明原Workflow/Pod已消失；必须保留ModelDev原Namespace UID、Workflow UID及writer终止的独立证明，不把更多RBAC当作清理证明。
5. 实际探针必须按表中真实name和v1beta1构造SAR，或以真实projected token发对应GET。401、403、空列表、查错资源pipelineversions、把UUID放name、仅--as权限允许都不是正确隔离通过证据。
6. shared-read必须保持既定关闭；resource_manager.go:1917–1922 在shared-read打开时跳过get/list授权，本设计最小角色和隔离验收都不依赖该旁路。

## 6. 固定官方源码

全部使用同一health SHA：

- https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/apiserver/common/const.go
- https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/apiserver/server/experiment_server.go
- https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/apiserver/server/run_server.go
- https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/apiserver/server/pipeline_server.go
- https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/apiserver/server/api_converter.go
- https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/apiserver/resource/resource_manager.go


## 7. KFP Stop的实际字段

同一SHA的backend/src/common/util/execution_spec.go:281–288，GetTerminatePatch生成spec.activeDeadlineSeconds=0的merge patch。本项目native策略必须允许精确KFP API principal将其设0，不允许提高或移除原限制；不能错写成KFP采用spec.shutdown。正常Argo全对象UPDATE在受保护spec不变时也必须通过。
