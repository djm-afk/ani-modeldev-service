任务专用 admission 扩权审查材料（尚未注册）

处理器位于 [admission.py](admission.py)，生成与只读 server dry-run 入口为
[render_admission.py](render_admission.py)。复用实际部署的
`ani-system/ani-modeldev-exit-retention-config` 的 AdmissionReview、TLS 和
`modeldev.ani.io/step-exit-evidence` finalizer 机制；旧源码、配置和 webhook 不变。

只匹配 `CREATE v1/Pod`；namespaceSelector 同时要求
`kubernetes.io/metadata.name=ani-ft-core-20261014` 和
`ani.io/managed-by=finetuning-core-flow-20261014`。objectSelector 要求 KFP v2
component、Workflow、Run 与编译器 `ani.modeldev/step-role` 标签。处理器再读取
namespace UID `8f79e7cc-769b-4b2d-87d3-a69c53c946f3` 以及原 Workflow 的 UID/Run
ID，核对 controller `system:serviceaccount:kubeflow:argo`、唯一 Workflow owner、
`modeldev-step` ServiceAccount、完整有限 immutable image allowlist 和角色挂载。

| 角色 | PVC 只读挂载 | PVC 写挂载 |
| --- | --- | --- |
| prepare | — | /workspace |
| export | inputs、training、reports/train | export、reports/export |
| verify | inputs、export、reports/export | reports/verify |
| collect、publish | /workspace | /workspace/publication（叠挂载） |
| workspace-name、train-wait、register、close、finalize-close | 无 PVC | 无 PVC |

所有 main/wait/init 容器根文件系统只读、禁止提权和 drop ALL；Pod fsGroup=10001。
所有 main/wait/init 容器 UID/GID=10001；main 保留原 CPU/内存预算，附加临时存储 request=10Gi、limit=12Gi。
非 main 的 PVC 挂载只读；ConfigMap/CA 挂载只读。保留真实退出证据 finalizer。

拟新增 11 个对象，名称均为 `finetuning-core-flow-20261014-admission`：

- system namespace：ServiceAccount、ConfigMap、独立 task CA 签发的 TLS Secret、Deployment、Service、NetworkPolicy。
- tenant namespace：只允许 GET Workflow 的 Role/RoleBinding。
- cluster scope：只允许 GET 该 namespace 的 ClusterRole/ClusterRoleBinding，以及上述严格选择器的 MutatingWebhookConfiguration。

NetworkPolicy 仅允许已核实 `.10/.11/.12` API 节点访问该 admission Pod 的 8443。
不改共享控制器、旧 admission 注册、全局 policy 或 `ani-system` 负载。

Fedora 行为测试和 server dry-run 已通过；未 apply/注册。第一次结构 dry-run 使用
历史已部署 step immutable digest，不能用于本任务 Release。最终同源码 commit 的
组件 digest 就绪后必须重新渲染准确 allowlist 并再次 dry-run，才形成可批准的最终材料。
完整含私钥的 manifest 仅保存在 Fedora task private 目录；公开预览只保留 Secret 名称/键名。
实际回执路径：`/home/chabking/workspace/finetuning-cpu-m3a-20261014/env/deployment/admission-dry-run.json`。
