# 给执行 agent 的提示词

请直接实施本执行包中的「ModelDev 动态租户执行环境开通与调度接入」，持续完成编码、必要验证、指定测试环境切换和结果交付。不要只复述方案或重新开展一次全量核验。

## 先读与任务跟踪

把执行包放到 ModelDev 仓库的 .scratch/modeldev-dynamic-tenant-environment/。先读取三仓各自 AGENTS.md、ModelDev docs/agents/issue-tracker.md、triage-labels.md，再按 README → spec.md → contracts → issues/01–10 的顺序读取。

以 spec.md 为唯一主规格。任务初始 ready-for-agent/open 不代表完成；按 Blocked by 推进，每项在 Result 中填写实际修改、验收ID、证据、未验证项后才能 resolved。允许按明确文件归属并行，不允许不同agent各自重新定义公共合同。

## 不可更改的决策

1. 一个共享 ModelDev 部署服务多租户。复用 tenant-<canonical lowercase nonzero resource_tenant_id UUID>，精确GET并验证network两标签、Active、原UID。
2. Namespace缺失直接错误；ModelDev和本期Res owner不创建、补标签、收养或回退；不新增Res Namespace查询/开通RPC。
3. Gov当前租户新增Ensure/Get，显式开通；Prepare/Import/Create不隐式开通。Ensure不占quota，不进Create可靠队列。
4. 一张环境登记表，同tenant+cluster唯一；pg_try_advisory_lock专用连接、25秒有限推进、typed receipts恢复；无新worker/lease/调度器。
5. 使用ani-system/modeldev-control projected token；固定四个Pipeline发布于ani-system；每tenant一个Experiment。token逐请求读，两个audience不能混用。
6. 固定对象、KFP artifact SecretRef、受管镜像authority/platform project、原SA UID、projection canonical和native策略均按spec；不要复制旧租户的配置/凭据或扩大权限。
7. 执行快照只支持v4，冻结DispatchOwner和ManagedImageAccess等材料；新IR四个根参数，driver和业务step都验证原Namespace UID。
8. 复用原Reserve唯一发送许可和close fence；未知发送不能重POST。多租户仅改现有bounded发现，坏tenant不能挡住其他tenant；已seal GPU outbox独立投递。
9. 没有真实用户，不做兼容。先结清原关闭/资源/占额并保存ACK，再删除旧ModelDev记录、专属资源及旧配置；不导入旧binding，不留v3reader或oldworker。
10. 不改Inference，不新建租户初始化、UI菜单、多集群选址、token发行、任意Pipeline执行或其他owner调度系统。

## 仓库与指定环境

仓库：
- zhangzhe-ctrl/ani-modeldev-service
- zhangzhe-ctrl/ani-governance
- zhangzhe-ctrl/ani-resource-service

spec列出了本轮核对的main SHA。开工再次fetch main；若变化，只复核相关diff并记录，不把旧SHA当届时最新。保留用户已有工作，必要时隔离worktree。

唯一测试SSH入口：kubercloud@100.64.128.12。
必须匹配：
- context=kubernetes-admin@ani-kcn-full
- Kubernetes API=https://lb.kubesphere.local:6443
- cluster_id=f39bf6e2-f3ed-4191-ada3-caeebf86c220
- kube-system UID=3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24

本地做轻量阅读/修改；重型构建、定向集成测试与仓库强制make verify在远端Fedora执行。git push使用SSH。不要访问其他集群，不向第三方发送消息，不输出秘密值。

已有任务范围内的代码、构建、测试服务更新和旧ModelDev清理继续做，不反复询问已确定方案。实际权限不足、事实矛盾或需要触及清单外owner时，先完成不受阻工作，再给出具体阻塞与最小所需输入；不能自行换架构规避。

## 执行与门槛

按01–10依赖推进，严格执行spec第13节清理顺序。原Cleanup API不清PVC/S3/业务记录；KFP DeleteRun成功也不证明Workflow消失。必须保留原UID和writer证据。对标准A/B Namespace和共存VM/Sandbox/PVC不做整片删除。

新增环境必须能在同一个进程不重启的情况下接入A和B。验收覆盖CPU一般训练、B的GPU_SHARED微调、运行中Stop、业务A/B互相越权拒绝、token轮换、动态镜像/STS、webhook故障时普通Pod共存、原GPU释放ACK。固定scope/owner/快照/错误合同的必要组件测试也必须通过。

templates中的null是精确输入缺口，不是自由选型。所有验收结果初始NOT_RUN；真实通过才填PASS。缺GPU/模型/权限时写BLOCKED与具体证据，不用CPU替换后声称全链通过。

## 最终交付

按templates/implementation-report.md报告，至少提供：
- 三仓commit、base diff、正式API module pin、生成物检查；
- image digest/imageID与源码对应；
- 已填非敏感deployment-inputs、最终部署模板和四定义ID/hash；
- Namespace UID回填与旧清理manifest摘要、原charges/outbox/ACK及计数；
- acceptance-results.tsv逐项结果和可核对原始证据；
- A/B environment/Experiment/execution/Run/Workflow/TrainJob/PVC原UID；
- 正式合同/ADR更新、旧代码配置删除结果；
- 任何仍BLOCKED或NOT_RUN的项目，准确说明影响。

实施前先给简短行动更新，然后开始第01项。持续推进到本期目标完成，不以“可以继续”结束工作。
