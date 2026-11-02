# 08 固定镜像、四定义与部署材料准备验证

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

准备同一份可验证的固定软件材料、发布工具和bootstrap部署包，为09一次切换提供确定输入。
依据：[spec.md](../spec.md) §1.3、§6、§10–11、§16；本项的定义发布限准备/验证，旧环境仍服务时不破坏其材料或权限。

## Scope

- ani-modeldev-service 的四份固定pipeline编译/发布入口、catalogue、镜像构建与材料manifest。
- deploy/modeldev-webhook/render.py、deploy/modeldev-tenant-policy/render.py与06固定bootstrap/native RBAC模板。
- control/webhook部署模板、projected token挂载、受管CA/步骤配置、KFP launcher和artifact SecretRef非敏感模板。
- 执行包 deployment-inputs.json、固定四定义/IR/material摘要表和可执行发布/回读验证记录模板。
- Res/Gov仅提供已固定接口和身份输入；不在本项修改其他仓库业务或上线新运行服务。

## Implementation

1. 逐项核实镜像、bucket/connection、workspace、公开endpoint/region、metadata host/port、CA key和TLS flags输入，未知项不猜。
2. 固定业务镜像进入该tenant space authority的受管platform_project，使用digest；A/B robot可读条件明确记录。
3. driver/launcher其他authority只有平台明确匿名且实际pull证据成立才允许无ref，否则BLOCKED/由平台统一镜像。
4. 用04编译器准备四份IR和固定profile；校验root四参数、完整IR SHA、组件/driver/support image及资源合同。
5. 准备ani-system中四固定Pipeline名称和ir-<前16SHA> Version的发布工具；陌生同名/完整摘要不符必须冲突。
6. 验证发布器的精确GET与幂等冲突分支；本项不删除/替换正在服务的旧Pipeline、Version或运行catalogue。
7. 生成同一不可变材料bundle供主服务/webhook/发布证明消费；最终实际Pipeline/Version ID在09发布后回填校验。
8. 准备control投影token文件挂载、每请求重读路径和两audience配置；禁止token值入包，不能用subPath固定旧文件。
9. 准备固定launcher SecretRef及独立artifact bucket凭据文件引用；模板只有两键schema与非敏感配置，不嵌入AK/SK。
10. 渲染精确ClusterRole/native VAP/Binding/全局webhook模板，验证先策略后create/bind的安装顺序和principals输入。
11. 准备普通Pod/ModelDev Pod、正常CPU/GPU与非法roleRef/key/ns的server dry-run用例，及09/10实际安装和恢复说明。
12. 输出待切换发布清单、镜像digest/构建commit、模板摘要、剩余live输入和前置门槛；不修改旧服务启动配置或撤权。

## Acceptance

- SNP01、AD02：四IR仅固定root参数，与v4/严格Workflow解析一致；主服务和webhook使用同一材料manifest。
- IM01：generic/fine refs及SA继承材料一致；固定业务镜像单authority/platform_project，无隐含前缀放行。
- LIVE02：bootstrap模板和正反dry-run用例可直接在09窗口执行；离线渲染/测试不能记现场PASS。
- LIVE04：发布器证明固定父ani-system、完整digest回读与实际SAR合同；最终发布ID/token访问留09/10。
- LIVE09：artifact SecretRef、CA实际加载、专用bucket权限和普通tenant隔离的验证步骤完整，未实测项标NOT_RUN/BLOCKED。
- LIVE01、LIVE06：记录可知构建/部署输入及待后续取值字段；本项不要求新环境先运行才解除09依赖。

## Constraints

- 08不切换主服务/webhook、不替换全局policy、不撤销旧权限、不删除旧定义/对象；可能影响在线运行的动作仅09执行。
- 不把准备/渲染/受控发布器测试声明为四定义已在目标集群发布或LIVE已通过。
- 不按tenant复制Pipeline或profile，不将发布写权限下放runtime control/Ensure。
- 不使用HTTP/disableSSL/skipTLS绕过CA，不复制业务STS/全权AK，不将共享artifactAK声称为tenant IAM隔离。
- 不扩大Inference/installer或共享ani-kfp-trainjob；构建与必需gate在批准远端，最终imageID映射由09/10补齐。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。四个固定新Version及最终bundle已正式发布seal并消费，三运行镜像/主服务已部署；publisher已撤权，见 evidence/10/finetuning-ir-correction-20261101/publisher/final-step67/actual-publication-window.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: IN_PROGRESS；四镜像、四共享Pipeline/Version、sealed bundle实际完成；最终platform12refs/新服务artifact I/O与真实pull仍未完成，不将整项resolved。
- Actual artifacts: 四Pipeline/Version于ani-system正常发布并全读回对比；canonical bundle manifest SHAa27e1669fd2657d2d46cc4ceb419c4420e1733e743798653e9e8d300e91c8ee6。四固定registry digest见报告§4及image-publication/result.json，不改该不可变原收据。
- Publisher identity: 用户已批准限定temporary SA/Role/RB/oneJob；已真实发布后撤RB/UID删除Job/Pod/SA/Role及投影token source，不在新tenant加入时再发定义。evidence/08/platform-materials-final/actual-publication-window.json。
- Artifact store: 独立共享artifact IAM仅限新internalbucket，三业务bucket/admin否定403实际通过；source Secret两键已具名。真实launcher I/O、rotation和ordinarytenant隔离NOT_RUN。
- Native/bootstrap: 21固定对象已真实安装、3VAP actual typeChecking/warnings0，11refs由生产Go实际hash。native bootstrap server dry-run14合法+7反例实际PASS，完整workload/globalwebhook行为不冒充通过。
- Current blockers: 新webhook新Pod在私有project5缺pull凭据而init失败，旧2Ready保持、MWC未切；旧ani-gpu-platform当前public=true而新project5=false均实际GET200。project-only只读pull新identity，以及128→162与精确trusted输入都依用户指示等待裁定，没有自行创建或采用。
- Tool cleanup: BuildKit/build私有工具已逐项actualabsence；已用完的publisher/原legacy工具与投影token source也已清；仍需要的renderer/management/window工具按总账保留待最终删除。
- Validation: 复用固定source必要gate，不因证据/文档重跑全量；最终材料真实IR/fullreadback已实际，服务可用/训练不以工具测试或HTTP200代替。

## Comments

以下为按时间保留的历史记录；当前状态以本文件 Result 为准。

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。

第二轮公开输入补证：evidence/08-public-inputs-readonly-20261101.json和公开CA原bytes(713bytes)；只读实际metadata Service派生新tenant候选metadata-grpc-service.kubeflow.svc:8080，原CM仅短名、旧namespace同名CM404。已取driver/launcher/Argo原digest，显式TLS flags未暴露保持UNKNOWN，不当false；未作新launcher加载/I-O。

第二轮镜像准备入口事实：evidence/08-workbench-build-only-readonly-20261101.json。线上app.js/request7字段与本地前端一致；本地后端拒额外字段且push后无条件deploy，但live后端hash未暴露不宣称同源。没有可安全使用的build-only操作，现有modeldev recipe仅主service，未涵盖webhook/step/training；WORKLOAD_IMAGE尚未固定。未POST job或直接绕开入口。需取得支持完整镜像发布的确切入口合同，实际构建与digest仍NOT_RUN。

新增本地镜像构建部分PASS：Fedora固定Git d807的689输入校验后实际构建step/webhook，缺配置拒绝启动，source/tree/binarySHA/localImageID/localOCIdigest完整见evidence/08/build/build-receipt.json；未push/rollout。CPU/GPU因指定store固定LFbase缺失BLOCKED。此产物阶段未走Workbench job，不更新测试环境；服务切换仍指定Workbench。原snapshot55差异已被guard拒绝，实际构建从固定Git普通文件context，无Fedora archive打包传输。
