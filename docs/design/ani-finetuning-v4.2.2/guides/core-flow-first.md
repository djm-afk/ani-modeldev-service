# 主流程开发与分段验收手册｜v4.2.2同源阅读视图

权威正文：`../spec.md`；SHA256：`5323612099bbfe54f2e13b3aea4a5c3c4f9a4cf143a64f7cc526b530f9925ec9`。本文件只切片原文，不独立修改；先改spec再导出。

<!-- source-section: A03; source-sha256: 2dcba5884dd2d944d8719cae7e46e7dcca980733728d84f74091523f9ea9630f -->
## A03 · 最终业务链与当前先做的执行主链

### 3.1 最终用户业务链（架构不变）

下图是最终集成目标，不是要求所有步骤的外部依赖在开工前同时就绪。

```text
管理员把 JSON 放到已批准 S3 范围
  → 受控导入：固定对象、校验内容、登记 READY

用户经现有 BFF 提交微调
  → 当前用户/租户/权限校验
  → 原键重放检查；新请求解析并冻结 Release、输入、基模、目标
  → 按本次合同/Release解析默认值与显式值，冻结实际配置
  → Gov 持久受理与可靠投递
  → ModelDev 持久 inbox，返回受理 ACK
  → 提交并绑定权威 KFP Run
      → workspace-name / CreatePVC
      → prepare：原 JSON、固定基模、安全解包、准备清单
      → train-wait：由 ModelDev 创建并观察唯一 TrainJob
      → export：独立 KFP Pod 合并本次 Adapter
      → verify：独立 KFP Pod 加载 merged 并短推理
      → collect：检查文件全集及打包选择
      → publish：上传候选文件和主模型 tar
      → register：先独立确认 publication，再调用 Model 登记
      → close：提交 NATURAL_TERMINAL，取得有效持久收据后返回
  → KFP 自然完成
  → 独立 owner/CloseWorker 确认计算结束，记录 CLOSED

本方案另行执行验收：经授权从 Model 取得本次新包，无训练 PVC，使用现有 Inference 实际调用
```

**正常业务链不能由管理员逐次手工串接。**管理员负责预设、镜像、基模、数据导入和例外处置；每次正常提交后的计算、发布、登记与关闭由程序自动执行。

| 边界 | 能证明什么 | 不能宣布什么 |
|---|---|---|
| Model 未正式就绪时的开发/分段验证 | 真实负载、模块、发布与关闭及Model消费者合同可逐项验证 | 已有可信Model登记；已完成用户完整微调闭环 |
| 当前执行与产物主链 | 在隔离装配中复用正式ModelDev持久化、真实KFP/Trainer及原Pipeline；实际训练/export/verify、发布确认、产物取回和关闭通过 | 上游真实BFF/前端、未接入的Model或整版验收已通过 |
| 单次执行成功完成 | 真实训练/导出/verify，固定远端publication，原ModelVersion READY且一致，最终CLOSED；按A10处理停止与恢复历史 | 本方案已全部验收，或用户已自动获得一个新推理服务 |
| 本方案最终验收完成 | 本版应交付用例、相关回归与交接完成；FT29/C23从训练卷外独立使用本次新包真实调用 | 未做的CSV/上传/多框架/GPU同时已支持 |

**Model未接入时不增加永久的“只训练不登记”公共微调模式。**公共FINETUNING完整能力保持未启用。内部主链验证可以在发布确认后观察真实的依赖未接入错误和计算关闭，记录“执行与产物主链通过、Model登记未接入”；不能把该执行改为A10六项齐备的业务成功。已受理后的Model临时故障继续走原失败/恢复路径。

### 3.2 当前先验什么（不是第二条生产流程）

```text
隔离测试驱动：正式codec/程序合同 + 固定候选执行来件
  → ModelDev正式应用用例和真实数据库持久记录
  → 原KFP Pipeline → 唯一Trainer TrainJob → 真实LoRA
  → export → verify → collect → publish
  → register前半段：真实确认publication
  → Model可用则真实登记；未接入则明确不可用，不返回假READY
  → 复用正常或失败关闭路径，确认实际计算关闭
  → 不挂训练卷，从持久对象取回并校验真实产物
```

上游测试来件不代表Gov已经认证/授权，也不冒充Gov持久账本或生效指针。只有明确声明的上游/外部边界可以替身；原工作负载身份、权威Run绑定、真实写者和对象核验不能用假成功代替。具体测试装配、数据/基模来源和证据分层见D12.6，当前阶段完成清单见D11.6。真实BFF/前端及FT29独立Inference留在随后阶段；原合同应现在固定。

<!-- source-section: A12; source-sha256: 175d1f22ce0c37188284152164afee0734c381bcca01faf71bb1d0e1589cbf03 -->
## A12 · 主流程优先的开工顺序与阶段声明

**当前先交付“微调执行与产物主链通过”。真实BFF、前端、候选经BFF进入验收的操作说明后置，不是W0/W2/W3/W4开工或M3-A通过条件。**W1现在必须交付正式结构、参数语义/解析和快照合同，不能最后再考虑上游兼容。后置工作可并行编码，但不能反过来阻塞主链。[S12]

| 阶段 | 交付目标 | 当前不等待什么／可声明什么 |
|---|---|---|
| M0 负载与合同候选 | Fedora先跑基准，再跑一组非默认候选；锁entry/指标/配置/文件/预算；W0/W1协作 | 不等完整BFF、Model或前端；只证真实负载与相应合同 |
| M1-A ModelDev增量 | 正式应用用例、真实数据库、输入/参数解析/原快照、KFP/Trainer适配、export/verify、发布确认、查询投影、关闭；测试装配 | 不等BFF listener/页面验收；Model边界可显式替身但不标MR通过 |
| M1-B Model对接（独立依赖线） | 提供方正式API；Gov规范调用、固定对象、具体登记派发仲裁及真实读取/登记/查证 | 按能力逐项接入；不阻塞不依赖Model的M0/M1-A/M3-A片段；没有真登记不声称单次成功 |
| M2 当前正确性与兼容 | 主链幂等、启动/停止/deadline/重启、实际指标、参数拒绝/组合、产物保留；R1在登记实现前落地；相关CPU回归 | 与开发同步，不等BFF最后一轮才测；上游权限与真实Model的未测断言单列 |
| M3-A 当前目标集群主链 | .10～.12真实ModelDev、数据库、KFP/Trainer、S3和实际进程；基准及非默认真实执行、发布确认、脱卷取回、停止关闭 | 按D11.6报告CORE_FLOW阶段证据；不要求BFF/前端或FT29先通过；Model缺失按D12.6明确报依赖错误并保留发布结果 |
| M3-B 后续真实BFF/前端 | 已有BFF认证授权/当前绑定/持久投递/查询/停止、两参数实际透传、已有页面；此时落实候选绑定和回退说明 | 复用主链，不重写训练；补PA05及其他含BFF/页面的断言，不拿夹具代替 |
| M3综合／M4最终 | 所有本版适用FT/PA/MR与相关回归、FT29/C23真实Inference、非原作者交接及正式启用材料 | 按D11.4报告SCHEME_FINAL；不删除后置项，不把单次/阶段通过当整版通过 |

这些名称表示研发阶段和证据范围，不是增加业务状态机或新的产品发布系统。M0/M1/M2与M3-A可按具体能力迭代合流；不要求先写完所有模块才联调。Model就绪可提前接入，不要求人为等到BFF阶段。

**三类机器保持：**本机只阅读/编码/文档/Git及受控转运；既有`fedora`编译机负责生成、编译、模块测试、镜像/Pipeline构建和真实负载；172.16.101.10～12所属K8s运行实际部署、KFP/Trainer及集成驱动/独立取回作业。具体入口/路径按D12核实，不猜Fedora IP或把构建搬到训练节点宿主机。

管理员准备批准材料、配置和测试范围；测试驱动只触发一次业务应用用例，不逐Task手工建Job或搬权重。最终正常用户链仍一次提交后由程序自动运行。

文档版本v4.2.2不是新增代码、Fedora负载、主链、BFF、Model、GPU或方案验收通过。计划初始NOT_RUN；当前仅更新文档与派生检查。

<!-- source-section: D11.6; source-sha256: 5c9e32d2368b0e9e12a9a2d8a182707bfbbb0539d63c1eb9b3c99ffbc64af61d -->
### 11.6 当前主链、后续入口与最终验收的执行映射

以下scope只写在测试/进度记录中，**不是新的业务状态、API字段或运行模式**。FT/PA/MR保持原编号与完整断言；一项含多段时分记录coverage/未测断言，不以本域测试PASS覆盖整项。按排期后做填NOT_RUN；只有实际前置缺失才BLOCKED；只有本版真正不交付的上传/GPU等才DEFERRED。

| 证据范围 | 当前或后续的完成条件 | 关联用例与尚不能宣称 |
|---|---|---|
| WORKLOAD：M0 | Fedora上基准和非默认真实entry/LoRA/export/verify、指标/summary、参数消费与资源材料 | FT11/13/14和PA04/08/09的负载部分；不证KFP/Trainer、Gov授权或Model |
| CORE_FLOW：M3-A（当前阶段） | 真实ModelDev应用/数据库、KFP/Trainer、S3；READY/原快照/唯一训练、export/verify、发布确认、脱卷取回；失败/幂等/停止/deadline/重启及相关CPU回归；先基准再非默认 | FT01–04的本域部分、FT11–17、FT22–24、FT26–28/30/32的适用执行部分，以及PA01/03/04/06/08/09执行侧；未验证的Gov/Model/页面子断言单列 |
| MODEL_INTEGRATION：M1-B及综合 | MR01–08实际使用的正式API/身份/数据库/固定对象与登记查证；登记派发竞态对应真实验证 | FT07/08/10/18–21/25/28中Model断言；不要求先完成页面，也不自动证明Gov用户链 |
| BFF_INTEGRATION：M3-B（后续阶段） | 真实BFF用户认证、当前绑定/默认值、目标使用/写权、幂等冻结与投递、查询/停止/恢复，实际前端字段行为；候选绑定按D12.7 | PA05及PA02/03/06/07/10的上游/页面断言；FT05/06/09/25/26/31对应入口事实；不得拿测试来件或静态页面抵扣 |
| SCHEME_FINAL：M3综合/M4 | D11.4全部适用断言与回归、真Model、真实BFF/前端、FT29/C23新模型独立Inference、交接 | 保持整版原承诺，不要求每次任务自动部署推理 |
| GPU_PRECHECK：独立准备 | 仅D10.2约定的合同/字段与有条件渲染/设备观察 | 不阻塞上述任何CPU阶段，不代替FT/PA/MR |

**M3-A主链阶段最小收尾：**至少一份实际发布的模型tar/Adapter/指标/summary可脱卷取回并核对；真实训练/export/verify前序事实完整；实际停止不遗留计算；原请求重放、重启与deadline不造成重复计算；基准与至少一个非默认配置的实际消费可关联。第一条基准成功可先作为检查点，不要求首次试跑一次满足所有阶段用例，但完成主链阶段前需补相应正确性和非默认检查。

Model未就绪时，保留register前半段的真实publication确认；后半段明确报依赖未接入，再以原失败/关闭路径收尾。测试可以验证“PUBLISHED + 依赖错误 + CLOSED”这一预期结果而报告CORE_FLOW片段/阶段达标，**不能将该执行写成A10六项齐备的成功或将MR/FT29标PASS**。基模真实读取未接入时使用的批准素材及替代边界另记，FT10真实Model部分保持未测。尚未实际跑KFP/Trainer时只能报WORKLOAD/模块通过，不能报CORE_FLOW完整阶段通过。

FT22正常收尾的本域收据/自然关闭分支可先做模块验证；Model未就绪导致当前完整Pipeline以依赖错误收尾时，不将该失败路径充当FT22真实正常成功链，完整正常链随Model接入补测。

真实BFF未就绪不妨碍现有正式合同的解析与本域投影测试；但Gov绑定/权限/可靠投递必须由后续真实链证明。后置事项在`records/acceptance-plan.json`及实际结果中保留未测，不能因阶段达标而删掉。上述映射用于排期，不变更用例原语义，不能把所有FT标签统一标成CORE_FLOW后整项通过。

<!-- source-section: D12.1; source-sha256: 5e62337e0aabb48a4801c1eda6fbc13b465657adc2e413b7f8ffdf55e66f1e5d -->
### 12.1 三类机器，不再混为一处

| 环境 | 允许职责 | 不能作为哪种证据 |
|---|---|---|
| 本机/本地源码工作树 | 阅读、编码/文档编辑、Git提交推送、发起受控远程操作与转运制品 | 不本机编译、代码生成、单测、容器构建或训练 |
| **Fedora编译/验证机** | 既有SSH配置的Fedora；生成/格式化、编译、测试、Pipeline编译、镜像构建、临时数据库模块验证、真实LoRA负载验证 | Fedora边界替身闭环不等于目标K8s业务通过 |
| 172.16.101.10～12所属K8s | 实际部署、KFP/Trainer/存储/权限/停止与独立取模联合验收 | 不替代Fedora编译机，不在节点宿主机编辑/构建或裸跑服务 |

沿用已有 `fedora` SSH入口及当前批准的工作目录。具体地址/源码路径/测试数据库/镜像缓存引用来自本任务现有受控记录，不能直接把其他installer任务的目录当成modeldev目录。若缺少本任务值，核实已有SSH/任务记录后登记，不猜值、不移到集群规避。

K8s三节点本机可直连；从现有授权context核对API端点、集群标识和节点InternalIP，不猜`.10`必是控制节点或API固定6443，不用旧192.168.*环境。远程构建必须检出已推送的明确commit，生成差异回本机审阅后提交。保留已有临时数据库/容器清理流程，只清理本次资源。

<!-- source-section: D12.2; source-sha256: 0bccc01ee40948b7c23de72b84efd4e489b58f5bd392a0e18154985299df93cc -->
### 12.2 方案从哪里读取

沿用附件v3给出的单一权威相对路径：

```text
<实际 ani-modeldev-service 工作树>/.scratch/finetuning-cpu/spec.md
```

本交付包`spec.md`为完整v4.2.2正文；两册HTML、主流程开发手册、参数/前端视图、GPU边界、Model需求、W0–W6及验收计划全部同源。采用前保留v4.2.1与已有实际进度，再将新稿放入已确认唯一权威位置。若`.scratch`按当前AGENTS属于不入Git目录，遵守仓库规范，不为“提交方案”强行git add -f；正式文档或受管记录按现有约定保存。历史原件放reference，不并行编辑两份规范。当前生成**没有修改用户仓库**。

如实际任务已改用ANI-doc中的另一个权威位置，先核对并在任务记录登记唯一位置，再更新所有引用；不同时维护`.scratch`与ANI-doc两份可编辑“主方案”。旧CPU-P01/ENV任务包保留其原路径与职责，本版不是新的总控goal，不应把全套历史goal拼接执行。

开工记录应包含实际方案绝对路径、文件SHA、各仓branch/commit、Fedora工作路径及集群交接来源。不得把`<实际工作树>`占位符当作已确认目录，也不能因缺一份环境交接就停止全部编码。

<!-- source-section: D12.3; source-sha256: a3d76b2a20948b8cbdc146c062caf7162e550c3bfe67ac66c36b0af67c2d4048 -->
### 12.3 七个工作包：主链先做，入口后接

| 工作包 | 当前先交付 | 后续真实联调与不阻塞边界 |
|---|---|---|
| W0 负载与材料 | Alpaca JSON、A/B候选entry实际消费、指标/summary、export/verify、文件和预算 | Fedora材料核实后即可做；不等BFF/Model/页面 |
| W1 合同与受理 | 正式kind/codec、规则/默认/组合、意图与快照、共享解析和安全投影；为主链提供确定来件合同 | 实际Gov绑定/受理投递与BFF/前端放M3-B；W1这部分未完成不阻塞W0/W2/W3/W4 |
| W2 输入与准备 | 真实JSON验证/持久READY、固定字节与物化、固定基模读取端口 | S3/卷相关条件满足即联调；Model读取未就绪按D12.6限定素材，不冒充MR通过 |
| W3 执行与关闭 | 正式ModelDev持久执行、唯一Trainer、KFP export/verify、参数消费、启动停止原子仲裁、deadline/重启/实际关闭 | 目标组件可用即M3-A；不等真实BFF/前端；与W4共用执行停止边界 |
| W4 产物与ModelDev登记接缝 | HF真实收集/tar/publication确认、原登记记录/接口，D09.3逐副作用派发仲裁及故障测试 | S3主链先通过；Model未就绪不阻塞发布确认/关闭，真实登记等待提供方；不能假成功 |
| W5 Model提供方 | MR01–08需求和正式接口评估/交付，固定引用/可靠登记/查证/保护 | 独立提供方线；不是ModelDev作者必须同步改两仓，不成为主链全局前置 |
| W6 分段验证与交接 | 当前CORE_FLOW用例、竞态/参数组合向量、主链测试驱动边界与证据；保留FT/PA/MR原表 | M3-B再做真实BFF/前端及候选绑定；最终补FT29与交接；GPU独立不阻塞 |

已完成公共能力先复用。测试驱动由W3/W6在ModelDev现有测试装配中落地，不新增W7、不新增服务。人员与工期另行确定。环境缺项只阻塞相关LIVE动作，继续合同、编码和Fedora测试；不能把“合同先定”扩大成必须等所有上游/页面一起完成。

<!-- source-section: D12.4; source-sha256: 69f1016892a41e0e95d33d20c2c57e92c6252cbf63f16bf4e73a007e0e022e60 -->
### 12.4 包内小任务与会合点

| 子任务 | owner | 当前阶段／完成物 |
|---|---|---|
| W0-P1 | W0＋W1 | M0：基准/非默认候选、真实框架映射、最终配置覆盖与指标；不等完整BFF |
| W1-P1 | W1 | M1-A：两字段程序合同、Release规则自校验、kind分流、旧CPU黄金回归 |
| W1-P2 | W1 | M1-A：共享默认合并/组合与存在性、原快照/摘要；真实Gov事务/投递接线在M3-B补证 |
| W1-P3 | W1 | M1-A：修正真实类型投影、预设/详情DTO与安全回放样例；不声称已对接页面 |
| W1-P4 | W1＋现有前端 | M3-B：真实BFF当前绑定、权限、受理与投递、查询/停止/恢复、页面提交与历史展示 |
| W3-P1 | W3＋W0 | M1-A/M3-A：entry消费原快照、summary回读、A/B真实训练与新增步骤 |
| W3-P2 | W3＋W6 | M3-A：D12.6隔离应用层测试驱动、真实持久化/KFP/Trainer/发布确认/停止；不新增生产API |
| W4-P1 | W4 | M1-A/M3-A：动态权重SHA与固定文件结构、tar/publication，Model未接入后的明确失败和计算关闭 |
| W4-P2 | W4＋关闭负责人 | 真实登记前：D09.3逐写派发与停止/时限仲裁、原收据/恢复、FT25.1–3 |
| W6-P1 | W6 | 当前：CORE_FLOW适用断言、PA共享解析/实际消费和R3向量；后续：BFF/页面剩余断言及整体汇总 |
| W6-P2 | W6＋BFF/ENV负责人 | M3-B前：D12.7候选隔离绑定/身份/回退记录；不是M0/M3-A前置 |

这些是原包内有限拆分，不增加产品范围或新的平台。W0/W1先形成候选合同可并行；完整候选等真实PipelineVersion/镜像生成后拼齐，不填假ID。真实环境先接通ModelDev→KFP/Trainer的主链，再用相同合同接回BFF；不因后置而改旧快照或引入第二默认指针。前端可并行开发，不得要求主链等它验收。

本机仅编辑/Git，Fedora构建/验证，目标K8s运行。先读AGENTS和工作区状态，使用本人分支或独立worktree；不stash/reset/clean/强推/自动合并，不改ENV agent的installer工作树，不无授权升级共享控制器。

<!-- source-section: D12.6; source-sha256: d3703fe6d556c382ef37343ca84c456afeaf13fa66366a16f85922aec17ba2c8 -->
### 12.6 当前主链的隔离测试装配与启动指引

本节不是已存在命令清单，也不是生产旁路。**W3/W6先核实现有集成测试入口；不足时只补一次性测试驱动，在同仓测试装配中调用正式ModelDev应用用例。**不得新建常驻服务或公网“测试创建”API，不增加生产allow-direct-access开关。具体测试路径/命令/镜像在代码实现后回填记录，不凭本文猜已有命令。

| 动作 | 当前可执行做法／约束 |
|---|---|
| 1. 定位材料 | 记录本包spec的实际绝对路径/SHA、任务仓分支commit、Fedora本任务路径、目标集群交接；只缺BFF资料不停止全部任务 |
| 2. 固定来件合同 | 复用正式codec、程序/Release解析与快照验证；测试材料显式选择候选ID/摘要/参数和批准输入，不另写宽松JSON或浮动默认值 |
| 3. 装配上游替代边界 | 隔离测试装配提供限定测试上下文/来件，进入真实应用层受理和数据库事务；记录入口在哪层。测试operation/identity不是Gov真实授权/持久受理证据，不能混入正式库或正式租户 |
| 4. 运行真实下游 | KFP/Trainer/实际步骤Pod与训练程序、真实数据库/S3必须是本次被验证层级的实际依赖；既有workload认证、权威Run/Task/UID和写者证明不关闭 |
| 5. 查、停、恢复观察 | 测试驱动调用同一查询/停止/对账用例，经过真实持久停止与关闭；不直接删Pod代替业务Stop，不手工改库造状态。BFF动作授权仍未验证 |
| 6. 发布及Model边界 | 保留register前半段真实publication核验。Model未接入时仅其明确边界可返回不可用，记录真实错误/关闭，不默认READY；真实Model可用则按已登记调用接入 |
| 7. 独立取回与交接 | 以批准的只读测试身份从原持久对象取回校验，不挂训练卷；它证明存储独立性，不证明经BFF下载授权或经Model/Inference使用。后续对应断言再测 |

代码生成、测试驱动编译、镜像/Pipeline编译在既有Fedora完成，固定已推送commit；需要真实KFP/Trainer的驱动/被测程序部署到获准隔离K8s范围。Fedora临时数据库或边界替身模块测试可以先做，但不计目标集群主链完整通过。测试资源名称/数据库/namespace隔离、凭据引用与清理范围依现有授权，不让测试驱动访问正式资源。

上游RPC若只接受Gov证书，**保持这个正式拒绝规则**；从应用层测试装配替换上游，不借用Gov证书、不给普通调用方白名单、不把body tenant/actor直接作为正式Principal。测试装配没有经过的认证/委托/可靠投递断言全部NOT_RUN。若现有代码无法隔离装配，先补同仓测试接缝，不能用改库或关认证省事。

Model读取未接入时，素材必须有获准来源、固定字节/摘要与测试归属。可以用明确的只读Model边界fixture/获准素材适配驱动实际准备，不把虚构ModelVersion写入正式Model，也不声称MR02/FT10整项通过。数据READY则由真实本域导入用例产生，不直接插成功行。

**候选配置不是第二个生效指针。**主链测试输入显式固定一个候选，并通过相同校验；ModelDev不维护新的“当前Release”。Gov正式指针不修改。若现有Pipeline完整材料尚缺，可先做负载/局部adapter测试，补齐真实ID后才做相应主链；不能拿占位符换取受理。

实施后回填：测试源码/命令、源commit和镜像、入口应用层方法、替代边界、实际/未测依赖、隔离数据库和Namespace UID、候选摘要、Run/TrainJob/Pod/对象事实、停止/取回证据。现有`work-progress`/`acceptance-results`可附加这些信息，包内模板仅帮助记录，不要求新增业务表或另一套运维产品。

<!-- source-section: D12.7; source-sha256: 2fef506e669740bfa5f870411654ac90053c3a3480ce853b282f3bace6ff25b8 -->
### 12.7 后续真实BFF联调的候选绑定（不作为当前主链前置）

**只在准备M3-B时落实本节。**W0/W2/W3/W4和M3-A不等待候选经真实BFF调用的操作说明、页面或上游接线。最终用户入口仍是BFF，这里是阶段安排而非永久删除。

| M3-B前需要记录 | 采用规则 |
|---|---|
| 候选隔离范围 | 选择现有系统实际支持的隔离测试部署或受管绑定；不假定已有逐租户Release开关，不建设新的候选发布平台 |
| 真实入口和身份 | 已有BFF及真实认证/权限/租户映射，明确获准测试用户；不增加绕过授权的测试RPC |
| 固定版本与变更所有权 | 候选release/digest/generation、当前指针的原值、具体负责仓库/配置/操作人；指针仍由Gov负责 |
| 参数/查询/停止路径 | PA05及其他含BFF/页面的断言走真实来件和投递；对照原快照与entry实际配置，不仅检查UI显示 |
| 回退与清理 | 记录实际回退方式；恢复前核对当前绑定仍为本次值，避免覆盖其他agent新变更；不停止无关历史执行、不删已发布模型 |

候选在隔离测试环境通过真实BFF验证，不等于向普通用户正式启用；M3-B及最终验收前后记录清楚。已经通过的M3-A记录继续保留，新增上游引发的真实错误仍需修正并回归，不通过宣称“主链已过”跳过整体链。Model若仍未正式接入，只能记相应BFF部分，PA05全链/MR/FT29继续未测。


## 来源与证据口径

本稿直接基于用户上传的**完整v4.2.1包**，按两轮审核/讨论改写登记派发仲裁、主链优先顺序和组合测试，见S11/S12。未请求重新调研技术选型或最新代码，因此本次不借外部资料补写未核实实现。历史S0–S10保留其版本来源；本次新增是设计修订，不是当前分支/硬件的审计事实。无业务源码修改、依赖发布、Fedora训练、K8s/Model/Inference/GPU验收；文档检查不写成业务PASS。

### S0 · 用户附件《ANI CPU 受管微调方案 v3》

沿用原v3附件；内容包括单TrainJob、KFP export/verify、原受理/发布/关闭复用、Model需求、资源预算及原验收映射。原文副本位于 `reference/spec-v3.md`。其中指标适配职责是v4.1指标意见的原依据，不与本次421-R2候选BFF排期混同；已被v4收窄的CSV等范围不恢复。

### S1 · 模型研发平台 v0.4

《精简业务与架构方案》《精简开发实施说明》《管理与操作指导手册》是平台范围背景；v4形成时重点核对开发册D16的C01–C34，本次保留其原含义及已明确的覆盖边界。v0.4不是所有内容均已实现的报告。v0.2/v0.4已含target_model_id/new_model_name两个目标分支。

### S2 · Governance统一调用合同（沿用v4固定引用）

仓库 `zhangzhe-ctrl/ani-governance`，固定commit `34388cf90d0834da76c9e5701ade5d92a248144f`，`docs/contracts/service-to-service-calls.md`，重点§1–3与§5–6。本规范涵盖三种调用方向，并明确规范入仓不代表存量实现全部符合。本次沿用v4已经固定的合同引用，未重新拉取当前分支，不宣布实现已全部对齐。

固定来源：<https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/docs/contracts/service-to-service-calls.md>

### S3 · ModelDev已有机制（沿用前轮固定源码核查）

仓库 `zhangzhe-ctrl/ani-modeldev-service`，固定commit `5b7fd05d9a74a8d0c65577e71b9a5c2db5dc8163`。主要位置：`internal/component/io.go`、`runner.go`、`internal/biz/execution_state.go`、`execution_closer.go`、`managed_runtime.go`、`internal/data/runtimeproof/proof.go`、`tasks.go`、`owner_close.go`、`internal/data/workspace/pipeline_io.go`、`pipelines/cpu_p01.py`、`README.md`。

固定来源：<https://github.com/zhangzhe-ctrl/ani-modeldev-service/tree/5b7fd05d9a74a8d0c65577e71b9a5c2db5dc8163>

本轮没有重审所有文件或当前默认分支，也没有复跑其测试。涉及正常关闭、Task核验、状态、Fedora职责的事实沿用前轮已读取源码，不作为新增微调已通过的证明。

### S4 · Model接口与实现（沿用前轮固定源码核查）

仓库 `zhangzhe-ctrl/ani-model-service`，固定commit `6473f1ec1386067e68dbd1294c10c660da7d1e7b`；`api/model/v1/model.proto`、`internal/service/model.go`、相关存储与身份实现。已存在方法不等于已满足本次可信接收、pending查证、固定引用和finalization要求；MR项以提供方交接及真实联调为准。

固定来源：<https://github.com/zhangzhe-ctrl/ani-model-service/tree/6473f1ec1386067e68dbd1294c10c660da7d1e7b>

### S5 · Model身份和名称唯一性（沿用前轮固定源码核查）

同S4：`migrations/000001_model_control_plane.sql`、`000006_external_model_id.sql`、`internal/data/postgres/model_store.go`、`queries/model.sql`及service错误映射。源码定义tenant内name唯一和外部model_id映射；本轮未连接实际库检查迁移状态。

### S6 · 本轮及紧邻对话的已确认决定

采用Gov统一S2S合同；Model需求先行、留接缝后对接；新模型/已有目标两分支；Alpaca JSON单格式；复用状态、deadline和正常关闭；不将审核澄清扩大为新系统。用户已有Fedora编译机，本机只编码/Git，172.16.101.10～12是目标集群。未提供或尚未核实的本任务具体环境值按D13处理。

### S7 · v4.1历史直接输入：v4与三项评审

v4原件在`reference/spec-v4.md`，v4.1的三项原评审在`reference/review-input-v4.1.md`。启动/停止仲裁、指标职责、单次执行与方案完成层级已在v4.1并入，本版继续保留D14.2对应正文，不因参数开放重写它们。

### S8 · v4.2的历史底稿：用户上传v4.1完整计划包

原包名：`ANI-CPU受管微调-v4.1-完整计划与复核包(1).zip`。其完整spec SHA256为`4c8430fc90ed92671ad06120113a909030ee76d70f807074790c5633f0b0de77`，本包按原字节保留`reference/spec-v4.1.md`。旧W0–W6是其派生视图，本版从新spec重新派生，不复制旧视图里已过时规则。

### S9 · v4.2参数审核依据与历史代码引用边界

`reference/parameter-review-v4.2.md`保留当时《ANI 微调v4.1：面向下一阶段参数开放的评审要点》原文；它是v4.2的历史依据，不是本次重新审核。P-01–P-05及W6六类验收已在D14.3映射；A07.4、D02.5、D05.3、D11.2.2为落实意见而新增的明确设计选择，不宣称来自原包逐字条款。

上一轮参数代码核对固定于ModelDev `c5fd93c60f4aa6af229ef39d3cc503396f6d0ffb`，涉及`contract/cpup01/intent.go`、`release.go`、`snapshot.go`，`internal/biz/admission_resolution.go`、`internal/service/materials.go`及`api/ani/modeldev/v1/modeldev.proto`。本轮沿用该审核事实，没有重新拉取/执行最新代码；它与S3较早的执行机制基线分开记录。固定来源：<https://github.com/zhangzhe-ctrl/ani-modeldev-service/tree/c5fd93c60f4aa6af229ef39d3cc503396f6d0ffb>。

本轮仅文档生成与一致性/呈现检查；无业务源码、Git、Model发布、Fedora训练、K8s部署或数据库迁移。文档PASS标DOCUMENT_ONLY，不写入FT、PA或MR运行结果。源材料未提供的实际端口/密钥/运行版本/安全范围不猜填。

### S10 · v4.2.1所用底稿与当时确认的GPU分期边界

底稿为已交付的`ANI-CPU受管微调-v4.2-参数扩展完整计划包.zip`，原spec按字节保留为`reference/spec-v4.2.md`，SHA256：`22e3c2af2424e4a2e7ef158171922979500229ffba2cf35558e701244a4af16b`。本轮用户先确认“GPU正式功能紧接CPU主干，合同与关键适配提前核对”，随后明确要求把“ModelDev公共接入位置/测试、v4.2小段边界说明”加入方案。

本次落实的原意是：Gov/ACC沿用既有规范，只核对ModelDev具体接入；ModelDev/Installer按条件做隔离渲染与设备前置验证；不强迫多仓当前启用GPU，不给CPU加假charge，不放松CPU限制，GPU未就绪不阻塞CPU微调验收。生成文档不是对远程写入、共享环境变更或设备占用的新授权。

Gov/ACC具体合同文档、接口字段、owner与证书配置由GPU准备负责人读取其正式仓库并记录确切commit；本轮不据旧讨论宣告当前实现已支持或尚不支持某RPC。`gpu-extension-boundary.md`只切片本规范，不成为新的GPU协议权威。


### S11 · 直接底稿v4.2.1与本次审核意见

原上传包：`ANI-CPU受管微调-v4.2.1-GPU后续接入边界完整计划包(1).zip`，SHA256 `5850581e31c29eaaaa8369751ca550302c06757dc3fea8608844701147713e25`。权威spec为1375行、SHA256 `363016708aef1c22454cbd320effead34b952aadf2bad08ea3a3582dc9e4c8b1`，按字节保留`reference/spec-v4.2.1.md`。审核意见原件保留`reference/review-v4.2.1.md`：R1具体登记派发仲裁；R2后续候选经BFF验证的操作说明；R3组合/默认黄金向量。本次未重审GitHub最新实现，不能把这些方案缺口说成已复现生产故障。

### S12 · 用户随后确认的执行顺序与本轮修订

用户明确“现在先完成微调主流程，真实BFF验收放到后面”，并要求据两轮讨论优化整包。落实为A12/D11.6/D12.6–7：合同先定；隔离应用层测试替代上游来件，复用实际后端主链；真实BFF/前端和候选绑定后置但最终仍验收；Model按原分期接入，不伪造READY；GPU独立准备继续不阻塞CPU。`review/review-input.md`记录这组输入摘要及原审核引用，不虚构对话原件或新增执行授权。

本次仅在对话工作目录编辑/生成/复核文档，没有连接用户本机、Fedora或K8s。最终代码/API编号、实际测试驱动方法、环境/凭据和有效参数批准仍需按任务验证并记录。本包全部业务计划初始NOT_RUN；历史已填记录应另存保留，不被新模板覆盖。
