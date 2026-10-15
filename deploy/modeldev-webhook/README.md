ModelDev 正式统一 Webhook

`cmd/ani-modeldev-webhook` 是同仓库独立 Go 进程，入口规则仅在
`internal/server/modeldevwebhook`。不访问业务 DB、S3、Model 或训练进程；不关闭、
删除或回收资源。旧 `modeldev.ani.io/step-exit-evidence` 标记保持兼容，释放仍由
ModelDev 原持久关闭契约负责。训练程序没有 Webhook RPC 依赖。

配置为 `ani.modeldev.webhook.v1`，见 `config.example.json`。其中 namespace UID、
镜像和 Release 文件仅为示例，不能用于部署。操作员必须使用实际 namespace
UID/owner label、受信 Argo creator、step SA、有限 profile 白名单与完整 immutable
image refs。每个 profile 校验正式 codec 的完整 canonical Release bytes/digest/kind；
FINETUNING component image 还必须等于该 Release 的 export/verify image。
未知或重复 JSON key、未知 profile、可变镜像、root UID、无界预算被拒绝。

组件 Pod 必须携带编译器明确的 `ani.modeldev/admission-profile` 与
`ani.modeldev/step-role`。Profile ID 是预分配稳定 DNS label，不从尚未完成的 IR
摘要反推，避免 Release/IR 标签循环。配置加载后冻结；新增 profile 使用新的
immutable ConfigMap 并滚动服务，保留所有尚在途旧 profile。

GENERAL_TRAINING 的 prepare/collect/publish 整卷 RW 保持原 CPU 流程；
FINETUNING 按角色限制 inputs/training/export/reports 子路径，collect/publish
仅 publication 可写。组件 main/wait/init 使用 UID/GID/fsGroup 10001、只读根、
drop ALL、禁止提权；只更改单独 JSONPatch 字段，保留其他安全字段、CPU/内存和
外部 finalizer。临时 storage 为配置中有界值，FT 固定 request 10Gi / limit 12Gi。

KFP backend driver 是独立有限留证角色。KFP 2.16 的
[container driver 生成源码](https://github.com/kubeflow/pipelines/blob/2.16.0/backend/src/v2/compiler/argocompiler/container.go#L174)
和 [DAG driver 源码](https://github.com/kubeflow/pipelines/blob/2.16.0/backend/src/v2/compiler/argocompiler/dag.go#L535)
声明冻结 driver image/command、`--type` 与 `--run_id`；组件平台 metadata 只加到
executor。Workflow 级 v2 标签也覆盖 driver，不能用 profile/role Exists 将它们
跳过。Namespace binding 必须显式提供 driver policy；只有真实原 Workflow/Run、
creator/SA、冻结 driver image+command、ROOT_DAG/DAG/CONTAINER、有限 support images
且无 PVC/host 挂载的 Pod 获得旧 finalizer 和 `step-role=driver`，不套计算挂载或
security/resource 修改。其他 KFP v2 目标缺 profile/role 会被拒绝。

Live namespace UID/owner label、原 Workflow UID/namespace/run 全部逐次 GET 核对。
若部署的 KFP backend 实际提供可信 pipeline-version label/annotation，配置
`pipeline_version_label` / `pipeline_version_annotation` 后强制等于 Release 的
PipelineVersionID；未配置时没有此项版本关联证明，不能用 Pod 的 profile 标签
替代 ModelDev 原工作负载的 Snapshot/Run/task/Pod 身份校验。

构建和验证只能在批准的 Fedora 环境执行：

```bash
mkdir -p artifacts
CGO_ENABLED=0 go build -trimpath -o artifacts/ani-modeldev-webhook ./cmd/ani-modeldev-webhook
go test -count=1 ./internal/server/modeldevwebhook ./cmd/ani-modeldev-webhook
./artifacts/ani-modeldev-webhook --check-config --config /operator-materials/webhook.json
podman build --network none --build-arg SOURCE_REVISION="$SOURCE_SHA" \
  -f deploy/modeldev-webhook/Dockerfile artifacts
python3 deploy/modeldev-webhook/render.py \
  --config /operator-materials/webhook.json \
  --webhook-binary "$WEBHOOK_BINARY" --webhook-image "$WEBHOOK_IMAGE_DIGEST" \
  --namespace "$CONTROL_NAMESPACE" --issuer-name "$APPROVED_ISSUER" \
  --api-server-cidr "$VERIFIED_API_NODE_CIDR" \
  --api-server-port "$VERIFIED_API_SERVER_PORT" \
  --kubernetes-service-cidr "$VERIFIED_KUBERNETES_SERVICE_CIDR" \
  --kubernetes-service-port "$VERIFIED_KUBERNETES_SERVICE_PORT" \
  --output /operator-materials/webhook-manifests
```

Renderer 离线生成 `service.json` 与单独的 `registration.json`，从不联系 Kubernetes，
也没有 apply/register 开关。Service 材料包含两副本 Deployment、PDB、有限 GET
namespace 的 ClusterRole、各接入 namespace 的 GET Workflow Role、固定输入
NetworkPolicy 和 cert-manager Certificate。正式服务使用项目 SA token/CA，不读
开发者 kubeconfig；Service 对外 443，Go TLS 8443。注册只选择明确接入 namespace
name+owner label 和 KFP v2 Pods，CREATE-only，Fail，timeout 8s。具体 API 节点及
Service ClusterIP 及两者实际端口均由操作员实测提供，两个端口参数均必填且
限定 1..65535，没有固定 API 端口默认值；其他 global policy 不受影响。

TLS 文件在新握手时热加载。cert-manager 声明 leaf 自动续期/换 key；Certificate、
Issuer 及 CA injector 在目标环境的可用性均为 **NOT_VERIFIED**。轮换文件暂不一致
时仅使用上一仍有效证书，readiness 失败；初始/存量过期证书不被接受。`/readyz`
检查当前证书与 namespace 身份/API 依赖；`/livez` 不检查外部依赖，`/metrics`
只给聚合计数。日志不记录请求体、UID、namespace、token、私钥或上游错误内容。
Body、并发、读写和依赖超时均有界。

后续部署须分别完成实际配置/image provenance、有限 RBAC、证书与 CA bundle、
两个后端 Ready/HTTPS/网络验证，再按明确授权注册准确 `registration.json`。
CA injector 注入行为需实际验证，空 CA bundle 不能算可用注册。先保留旧 hook
服务/注册；已发布旧 IR 不会被静默分配 profile。仅在新 IR/profile scope 生效且
存量执行逐一满足原关闭契约后迁移旧注册、退役旧服务；禁止批量删旧 finalizer。

本次仅源码和 Fedora 协议/行为验证，没有部署统一服务、注册 MWC、修改旧共享
hook 或启动 CORE Run。此前 task MWC 被用户拒绝的历史授权状态保持不变。
