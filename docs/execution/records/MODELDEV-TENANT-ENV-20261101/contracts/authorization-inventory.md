# 共享 Namespace 的 RBAC 授权边核验合同

状态：实施设计，尚未执行或验证。本文是本期 KFP artifact Secret/受管身份的有限前置检查，不是通用鉴权器，不增加认证服务、用户 token、任意 SAR/impersonate，也不由 ModelDev 撤销其他 owner 的授权。

## 1. 范围与唯一选择

Ensure 根据授权边核验当前目标 Namespace，而不是维护未来 tenant 的身份/凭据名单：完整读取目标 Namespace 的 RoleBinding、所有 ClusterRoleBinding，再精确 GET 它们引用的 Role/ClusterRole；检查其中对普通主体的危险授权。

只 GET 本服务固定 RoleBinding 不能证明不存在其他宽授权。只读取 Namespace 内 RoleBinding 也不能排除 ClusterRoleBinding 的全局授权。新增窄 Role 不会抵消其他 Role 的许可。

本期只接受部署输入已经核实的 Kubernetes `Node` + `RBAC` 授权模式及平台原生受信控制器/节点边界。执行 agent 必须记录 API server 实际 authorization modes/config、来源证据与摘要；不能根据 kubectl 能否访问或默认安装习惯猜测。存在未知外部 Webhook/ABAC/AlwaysAllow 等 authorizer、或无法确认实际模式时，返回 BLOCKED，不擅改 API server，也不把本扫描说成对外部 authorizer 的完整判断。

平台 Namespace、平台源 artifact Secret、集群管理员与节点凭据属于已批准的部署安全合同：其普通主体授权在 bootstrap 由部署身份核验，平台维护只由明列的可信 principal 操作。动态 Ensure 的授权边扫描覆盖**本次目标 Namespace + 全局 ClusterRoleBinding**，不递归扫描其他所有业务 Namespace，也不重新实现完整 Kubernetes 授权链。

## 2. 固定只读权限

读取的是 RBAC 配置中的 metadata、subjects、roleRef 和 rules；不读取任何 Secret data、用户凭据或 Pod 内环境。仅取 PartialObjectMetadata 不含 rules，无法完成此核验，不能拿它代替完整 RBAC 配置读取。

在已有目标 Namespace 的 `modeldev-runtime-v1` 追加以下只读规则，由已有 `modeldev-runtime` RoleBinding 授给 control，不新增每租户 Role/ClusterRole 或新的动态 bind 名单项：

```yaml
- apiGroups: ["rbac.authorization.k8s.io"]
  resources: ["rolebindings"]
  verbs: ["get", "list"]
- apiGroups: ["rbac.authorization.k8s.io"]
  resources: ["roles"]
  verbs: ["get"]
```

由平台部署身份一次安装下列全局只读角色与绑定；Ensure 不能创建或修改它，也不把它放入 runtime 的动态 bind 白名单：

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: modeldev-rbac-inventory-global-v1
rules:
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["clusterrolebindings"]
    verbs: ["get", "list"]
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["clusterroles"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: modeldev-rbac-inventory-global
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: modeldev-rbac-inventory-global-v1
subjects:
  - kind: ServiceAccount
    name: modeldev-control
    namespace: ani-system
```

这里必须允许按实际引用读取任意名字的 Role/ClusterRole；固定 resourceNames 不能覆盖未知的已有授权边。此项 tradeoff 是 control 可读授权配置，**没有**新增 Role/ClusterRole/RB/CRB 写、bind/escalate、Secret list、SubjectAccessReview、TokenRequest 或 impersonate。Namespace GET 与其他既定权限仍按主稿单独限定。

## 3. Ensure 顺序：凭据写入之前必须完成

固定顺序如下，属于现有有限 Ensure 的内部子步骤，不启动后台核验 worker：

1. 精确验证标准 Namespace 名、两项 network 标签、Active 与原 UID；确认已安装 native 保护策略及其批准摘要。
2. 按固定模板创建/回读**公开接入对象**，至少包括 `modeldev-runtime` RoleBinding，使 control 获得该 Namespace 的只读 RBAC 核验权限；冻结本次创建的三个 SA UID。此时不创建或刷新 `modeldev-kfp-artifacts`。
3. 完整执行本合同的两类 binding 列表和引用 role 读取；核对 native 的 reserved SA/Secret/Pod 保护已生效；得到本次有效 proof。
4. 仅在 proof 完整且未过期时，才允许第一次写入或后续刷新 `modeldev-kfp-artifacts`。再继续原 Experiment/VERIFY/PUBLISH/READY。
5. 最终 READY CAS 前复核 proof 的 Namespace UID、平台 policy/auth-mode digest 与有效期；保留主稿原 Namespace/SA/投影 live 检查。

发现宽读权限后才把环境设 BLOCKED，并不能挽回已经复制进去的共享 AK。因此**不得先写 Secret，再把授权审查放在最后的 VERIFY**。失败时保留已确认的公开接入对象供下一次恢复；不自动删除其他 owner 的 RoleBinding，不补一份窄 Role 冒充修复。

已 READY 环境的再次 Ensure 同样先重新核验，再刷新 Secret。若现有 Secret 已存在且发现冲突授权：先标 BLOCKED，保留原 UID/审计事实并报告；由原授权 owner 收紧，按主稿轮换协议处理可能暴露的凭据。BLOCKED 不等于撤回已经被读取的凭据，也不能删除原运行链的证据。

## 4. 可信 principal：平台固定集合 + 当前 Namespace 唯一步骤身份

可信集合只能来自已经核实的 deployment-inputs：ModelDev control/webhook、KFP API、Argo、Trainer、JobSet、Job controller、persistence、平台 maintenance，以及确有必要的精确控制平面/节点 principal。每一项必须记录精确完整 username 或 Kubernetes Subject 三元组、用途及来源证据；不接受任意 tenant RPC 提供这些项。

- 不允许按 `system:`、`system:serviceaccounts:`、某个 namespace 前缀或“有 managed-by label”信任一批主体。
- Group 只有作为明确的受信平台基础设施/管理员组、已记录其成员管理责任时才能列入可信输入；`system:authenticated`、`system:serviceaccounts`、`system:serviceaccounts:<tenant namespace>` 不能作为全组豁免。未知 Group 按普通主体处理，不需要查询其现有成员或索要登录凭据。
- ServiceAccount subject 的 Namespace 规范化按 Kubernetes RoleBinding 语义：RoleBinding 内省略时使用绑定所在 Namespace；ClusterRoleBinding 内无法确定 Namespace 的 SA subject 视为不完整并 BLOCKED。User kind 中完整 SA username 与同一 SA subject 按同一实际 username 比较。
- 当前 `system:serviceaccount:<standard tenant namespace>:modeldev-step` 可由 Namespace 名和固定 SA 名推导，无需未来 tenant 静态列表。但其例外只覆盖本方案批准的 `modeldev-step` RoleBinding、`modeldev-step-v1` 精确规则与原 SA UID；额外绑定给 step 的危险权限必须报告，不能因为名字相同全部略过。
- `trainer-workload` 和 `verifier` 不因 reserved 名字而自动成为可读 artifact 的可信主体；它们没有本期 Secret GET/受管 exec 权限。不得把三个业务 SA 一并列为无限制 trusted。
- 不得从这次扫描遇到的 ClusterRoleBinding、Role 名或已有 cluster-admin 授权自动扩充可信集合。遇到必要但尚未核实的平台主体，作为明确输入缺口 BLOCKED，补部署证据后再重试；不是让用户逐个 tenant 提交凭据。

可信主体本身仍受本方案固定 RBAC/native 保护及部署审查约束。本检查不声称抵抗已被攻破的节点、控制平面管理员或受信平台控制器。

## 5. 授权边枚举与危险规则

### 5.1 完整枚举

完整 LIST 目标 Namespace 的 RoleBindings 与全局 ClusterRoleBindings；两者都必须把 continue token 处理到空。RoleBinding roleRef.kind=Role 时 GET 同 Namespace 的 Role，kind=ClusterRole 时 GET 对应 ClusterRole；ClusterRoleBinding 只接受 ClusterRole。同一 role UID/resourceVersion 的重复引用可在**本次核验调用内**去重，不跨 Ensure 缓存。

读取 ClusterRole 当前 `.rules`，包含聚合角色已生成的实际规则；不根据 aggregationRule selector 自创另一套权限推导。记录该 role 的 UID/resourceVersion 与规范 rules digest。角色缺失、Forbidden、未知 roleRef/subject 形状、空或截断响应，都不是“没有权限”。

对每一个非可信 subject 检查其 grant。依据固定 Kubernetes RBAC 规则匹配 group、verb、resource/subresource 与 resourceNames；识别 `*` 和 Kubernetes 支持的子资源通配形式（例如 `*/exec`），不能只字符串精确比较而漏掉 wildcard。未覆盖的规则形式按核验不完整 BLOCKED，不试图编写通用 authorizer。

### 5.2 固定危险集合

| 授权 | 必须检查的范围与结论 |
|---|---|
| core secrets `get/list/watch` | resourceNames 为空或覆盖 `modeldev-kfp-artifacts` 时，普通 subject 命中即 BLOCKED；list/watch 不因 HTTP 不叫 GET 就忽略。只允许明确不包含该保留名的精确 resourceNames 通过此项。 |
| core pods/exec、pods/attach `get/create` | 普通 subject 在目标 Namespace 获得这些能力即 BLOCKED。GET/WebSocket 与 POST 两种实际访问都检查。为避免为未来随机 Pod 名建立允许名单，本期不为非空 resourceNames 增加例外；VM 自身 KubeVirt console/VNC 子资源不是本条 Pod exec。 |
| core serviceaccounts/token `create` | resourceNames 为空或覆盖当前 Namespace 三个 reserved SA 时 BLOCKED。全局 grant 还须覆盖平台可信身份所用 SA 名，防止从全局授权直接签发平台 token。 |
| impersonation | 对 User、Group、ServiceAccount 或认证扩展信息的 `impersonate` 能力不得授给普通 subject；本期保守 BLOCKED，不推断其可冒充身份以后一定无权限。检查 core 与 authentication.k8s.io 的相关资源及 wildcard。 |
| RBAC 提权能力 | 普通 subject 的 `bind`/`escalate` 授权或可直接绕过既有授权边的等价 wildcard 命中即 BLOCKED。不能只看到当前 Role 没有 secrets/get 就忽略它可绑定另一份高权限角色。 |

这是一份本项目的有限、保守前置条件；例如只允许 exec 一个既有普通 Pod 的精确授权仍可能使本项目 BLOCKED。不要悄悄加入按 Pod 名前缀放行的例外，更不要自动删该授权。原 owner 可选择收紧或让该 Namespace 暂不开通 ModelDev。

普通 Pod CREATE 能力本身不直接判冲突；对 reserved SA、artifact Secret 引用、受管 PVC、敏感 Pod UPDATE/ephemeralcontainers 的 native/webhook 规则必须先证明生效。否则，即使 Pod SA 没有 secrets/get，kubelet 仍可能替它挂载 Secret，授权边扫描不能代替这些准入规则。

### 5.3 必需的 legacy Token Secret 防借用规则

固定 native VAP 必须在**所有 Namespace**拒绝任意名字的以下 Secret CREATE/UPDATE：`type=kubernetes.io/service-account-token` 且 `kubernetes.io/service-account.name` 注解引用本方案三个 reserved SA；UPDATE 同时检查 oldObject，防止先改注解再退出保护。

本方案只使用 projected token，没有创建这种 legacy token Secret 的业务调用，因此无业务例外。该规则须在三个 SA 创建之前安装。普通主体即使能创建和读取自己的业务 Secret，也不能请 token-controller 替 reserved SA 填充长期 token。本期不为检查此问题增加 Secret LIST/GET，不复用旧 SA/旧 token；旧资源按一次切换规则收尾。

源依据：Kubernetes v1.35.0 `pkg/controller/serviceaccount/tokens_controller.go:113–115` 接收该类型 Secret，`319–321` 调用填充，`376–435` 生成数据并按 SA 名/UID 更新 Secret。不能只检查 TokenRequest RBAC 而漏掉这条标准路径。

## 6. 固定分页、体积、调用预算

- 每个 LIST `limit=100`，每类集合最多 10 页（目标 RoleBindings 最多 1000、全局 ClusterRoleBindings 最多 1000）；只有 continue 为空才完整。
- 同一次核验最多 GET 128 个不同 Role/ClusterRole；最多 8192 条规则。超过即本次核验不完整，不能忽略剩余引用。
- 单响应最多 1 MiB；RBAC 配置累计最多 8 MiB；拒绝重复/循环 continue token、跨目标 Namespace 的返回项和矛盾 UID/resourceVersion。
- 单个 Kubernetes 调用上限 2 秒；role GET 并发最多 4；整个核验最多 8 秒，包含 LIST、解析、规则计算与回执写入，计入 Ensure 25 秒总预算。
- 不在一次 Ensure 内自动无限重试、不启动后台扫描；可按既有显式 Ensure 重试恢复。核验请求不做写操作；只有完整结果之后才进入 Secret writer。

当前引用对象在核验中出现同名不同 UID/RV、LIST 一致性丢失/410 或字段变化时，不拼接两次观察为通过；本次返回 BLOCKED 后重新核验。上限是本项目实现预算，不冒称 Kubernetes API 原生限制。

确认存在危险 grant：`KFP_ARTIFACT_ACCESS_NOT_ISOLATED`，state=BLOCKED，输出有限非敏感诊断。权限不足、超时、缺平台输入、未知authorizer等无法完整检查时为 `ENVIRONMENT_AUTHORIZATION_UNVERIFIED`、state=BLOCKED；不能把“未证实”说成“已通过”。不返回任意上游错误正文。

## 7. 证明窗口、保存与持续权限变更边界

每次 Ensure 都执行一次完整核验，不能从上一次 READY 重用授权 proof。有效期固定为该次完整核验 `completed_at + 30s`，且仅用于同一次 Ensure、同一个 Namespace UID 的首次 Secret 写/刷新和 READY 发布；越过 deadline、Namespace UID、平台 policy/auth-mode digest 变化均作失效，不能续签旧 proof。

非敏感 proof 至少包括：tenant/cluster/Namespace name+UID、本次 operation_id、started/completed/expiry、authorization modes 与证据摘要、精确 trusted-principal 输入摘要、保护策略 UID/规范摘要、两类 LIST 完整性/页数、引用 RBAC 对象 name/UID/resourceVersion/rules digest、检查数、危险边列表或完整通过结果。摘要与有限 typed receipt 可进入现有 provision receipts；完整有限 JSON artifact 供交付审计，不把另一套可变授权数据库塞进环境登记。

这是一组明确时间窗口内的读事实，没有跨多个 Kubernetes 对象的原子锁。部署安全合同须规定：相关授权变更由原owner按受控维护清单处理；变更后的有效授权仍须满足本合同。需要引入危险权限时先排空并停用该ModelDev环境，处理原Secret及可能暴露凭据，不能只阻止新受理却保留可读共享AK后再授宽权；不得与Secret发布并发变更。此处不新建 RBAC 变更控制器，也不声称 Ensure 能实时捕获后续任意管理员改权。

原运行查询、Stop、close、cleanup 与已经 seal 的 outbox 不因本核验未通过而丢失处理路径；原 Namespace/对象 UID/fence 仍决定实际动作。权限检查失败只关闭新的 ModelDev 开通/受理许可，不把其他 VM/Instance 请求路由到 ModelDev 远程 webhook。

## 8. 最小有效验收

1. 在fake API/隔离测试夹具中，标准tenant C（不在静态A/B列表中）有安全的既有Namespace，Ensure完整审查后开通，无tenant凭据输入/静态用户列表；本条不要求在现场创建第三个Namespace。现场由原未开通的B证明同一实例动态接入。
2. 额外 RoleBinding、ClusterRoleBinding、对普通 Group 的 grant、被引用聚合 ClusterRole 实际 rules、wildcard、TokenRequest、exec 的 GET/POST 任一危险组合能使 BLOCKED，并指出实际 binding/role/subject/verb。
3. 模拟不完整 LIST、continue 循环、角色 404/403、体积/页数/规则/时间预算耗尽：BLOCKED；Kubernetes action 记录中没有 artifact Secret CREATE/PATCH。
4. modeldev-step 只有批准固定绑定时通过；给 trainer/verifier 增 Secret GET，或额外给 step 高权限绑定，均被发现。普通未知 group 的安全只读 discovery role 不因名字未知被误判为危险。
5. 普通主体可创建/读取自己的业务 Secret，但构造 reserved SA 的 legacy token Secret 被 native 拒绝；普通 Pod 挂载 artifact Secret、借 reserved SA、ephemeral 注入同样拒绝。
6. 现有冲突授权保持原状，ModelDev 返回 BLOCKED；没有 DELETE/UPDATE 别人的 Role/RB/CRB、没有 SAR、没有 TokenRequest/impersonate。授权 owner 修正后显式 Ensure 可重新通过。
7. proof 过期、原 Namespace UID 改变或同次读取发现 RBAC 版本矛盾时，不能继续 Secret 写入或 READY CAS；已有 outbox 仍能按原事件投递。
8. 准入和只读权限的正反测试使用真实 API server；普通主体探针使用原 groups。部署 principal 能读 A/B 是设计权限，不拿它当普通 tenant 越权失败用例。

## 9. 平台源码依据

- [Kubernetes v1.35 RBAC](https://v1-35.docs.kubernetes.io/docs/reference/access-authn-authz/rbac/)：授权许可叠加、绑定作用域、聚合角色及 resourceNames 限制；最终实现仍须按固定版本规则匹配做真实 API 对照。
- [Kubernetes v1.35.0 token controller](https://github.com/kubernetes/kubernetes/blob/v1.35.0/pkg/controller/serviceaccount/tokens_controller.go)：legacy ServiceAccount token Secret 的填充路径。
- 本任务已核验的 ModelDev 原能力与主稿固定 Bootstrap/Pod admission 合同；本文件没有宣称已对现场任意普通主体执行这些新测试。
