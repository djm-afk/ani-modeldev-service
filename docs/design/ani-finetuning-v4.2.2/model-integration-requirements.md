# Model需求与具体登记派发边界｜v4.2.2同源阅读视图

权威正文：spec.md；SHA256：`5323612099bbfe54f2e13b3aea4a5c3c4f9a4cf143a64f7cc526b530f9925ec9`。本文件只切片原文，不独立修改；先改spec再导出。

<!-- source-section: A04; source-sha256: a259004bae35ee8e721f8940ca75b193ade95b06ef2a35d43ff36dc290e861fe -->
## A04 · 职责与统一服务间调用：不另造协议

**Gov 已固定的 `docs/contracts/service-to-service-calls.md` 是内部业务 RPC 的通用规范。**本文只填写微调的具体调用关系与领域内容，不重新定义身份、委托、传输、重试或授权平台。[S2]

| 责任方 | 负责 | 不负责 |
|---|---|---|
| Governance/BFF | 当前用户认证；租户映射；模块/action/数据范围；原意图重放；Release 绑定；持久受理与投递 | 搬运权重、推进 KFP DAG、直接写 ModelDev/Model 数据库 |
| ModelDev | 本域校验与持久事实；权威 Run；唯一 TrainJob owner；准备/产物/Model adapter；执行观察与关闭 | 重新实现 IAM、另建工作流引擎或登记中心 |
| KFP | 正常步骤依赖、Task 执行、原始 Run/Task 与产物关系 | 决定用户权限或作为 Model 数据库写入者 |
| Trainer | 单 TrainJob 的实际训练工作负载 | 承担本次 export/verify，或替代 KFP 的完整流程 |
| Model | 基模/目标业务权威；固定模型引用；新建/版本登记；重放查证、finalization、删除保护 | 从普通 body actor/tenant 直接推断可信用户授权 |
| Installer/环境负责人 | 既定版本、Runtime、CPU池、存储、证书、网络和运行身份 | 代替每次业务创建 TrainJob 或伪造环境/业务通过 |

### 4.1 本次仅登记这些关系

| 调用 | 目的 | 信任来源 |
|---|---|---|
| Gov → ModelDev | 预检、持久命令、当前查询/恢复 | Gov 已认证主体及公布的调用合同 |
| ModelDev → Model：受理前 | 只读解析固定基模、目标与名称冲突 | 已验证 Gov 委托及其范围；不要求尚未形成的 execution spec |
| ModelDev → Model：受理后 | 读取已批准版本；按原产物/原目标登记；查证原操作 | 可信受理的持久事实；以 ModelDev 服务身份调用 |
| 用户恢复 → Gov → ModelDev → Model | 原登记的明确恢复 | 当前权限 + 原持久登记事实；不能更换目标/内容 |

每条实际关系补齐“调用方/接收方精确服务身份、完整 RPC、允许用途、租户来源、资源范围、幂等键、超时”。复用接收方公布的固定 API 模块；不复制生成代码、不跨仓导入 internal、不用本地 replace 冒充正式发布。参考代码不是本期新建共享运行时的理由。[S2]

### 4.2 两类既有执行协议不要混淆

ModelDev → KFP 的受保护可信身份头，以及 KFP 受管步骤 → ModelDev 的既有 workload/TokenReview 证明，是已有执行接入边界；不是让训练 Pod 冒充 Gov。其既有 listener/验证实现仅按新增步骤精确适配，不以本次微调名义重做全平台证书或入口迁移。

ModelDev → Model 等内部业务 RPC 必须按 Gov 规范接入已有业务 mTLS listener。KFP 组件不获取 Model 管理证书，训练程序不获取控制面凭据。缺依赖时明确未启用或失败，不换普通 listener、不回退明文、不跳过 TLS。

<!-- source-section: A06; source-sha256: 73d808029d0cc48e2ebc944f92b63a87b203b8f52f46d77b0cc08a2c3197a5cf -->
## A06 · 模型目标、对象身份与同名规则

### 6.1 两个目标分支，与最初平台方案一致

| 请求 | 唯一含义 |
|---|---|
| `new_model_name` | **create-only**：本次新建模型容器，再创建一个输出版本 |
| `target_model_id` | 给已存在、已授权可写的模型容器增加本次输出版本 |

两字段必须且只能出现一个。不按名称在登记时自动切换分支，不新建模型身份映射中心。[S0][S1]

本次新增微调请求中的 `target_model_id` 明确使用 **Model 返回的内部模型 UUID（Model.id）**；Model 旧 API 允许的外部别名不因此改变。适配器读回 Model.id 后校验固定目标，调用 Model 时也使用该 UUID。若已有消费者已经发布了不同的该字段合同，先登记冲突再对齐，不静默重解释。

### 6.2 同一操作重放与另一轮训练不同

```text
执行 A：new_model_name = cpu-ft-demo
  → 以 md-container:A 创建模型 M
  → 在 M 下以 md-A 登记版本 exec-A

A 的网络重试
  → 原 key / 原内容，找回同一个 M 和 exec-A

执行 B：target_model_id = M
  → 不调用 CreateModel
  → 在 M 下以 md-B 登记版本 exec-B

执行 C：new_model_name = cpu-ft-demo（名称已存在）
  → 名称冲突；调用者应新建请求，明确选择 target_model_id 或另一个名称
```

基线 Model 迁移已有 `UNIQUE(tenant_id,name)`，外部标识也有相应唯一约束；是否已在目标库应用，需要部署交接确认。本设计沿用该名称语义，不允许同租户同名堆积。[S5]

新受理时尽早检查名称冲突；**预检不占名**。最终创建仍由 Model 唯一约束判定。受理后出现并发同名竞争时，登记报冲突并保留产物、关闭计算；不自动换目标/名称/key。原执行重放必须先按原键查回，不能被自己的已建名称误拒绝。

若必须给已失败执行改投另一目标，本版没有该操作；管理员不能通过改库或重试参数暗中实现。普通用户改目标要新建业务请求，并承认那不是“原登记恢复”。

### 6.3 不增加多余 UUID

模型 UUID、版本 UUID、execution_id 分别是模型容器、具体版本、来源执行。外部 model_id 可能是普通字符串；version 标签 `exec-<execution_id>` 和登记键也不是额外模型对象。ModelDev 只保存必要关联与收据；不再给同一个 Model 容器生成另一套本域身份。

<!-- source-section: A09; source-sha256: ac56dcaddb861dd0205f11f2beed821b82ddd3889547932953eb5b3f134bf602 -->
## A09 · Model 未就绪时如何开发，何时才可启用

**Model和真实BFF都不是所有微调编码及执行主链验证的全局前置。**Model仅阻塞其真实基模调用/模型登记和相应单次执行成功事实；真实BFF/前端仅阻塞其接入用例。两者仍是本方案最终验收及相应正式启用的必要条件。先主链、后BFF，不降低最终成功标准。

| 阶段 | 可以做 | 必须标明 |
|---|---|---|
| 接口需求先行 | ModelDev 领域端口、输入输出约定、原登记记录、显式错误、测试替身；Model 提供方评估已有 API | 仅结构/行为合同测试；不是 Model 权限/持久化通过 |
| Model 接收方未就绪 | Fedora验证获准固定负载；隔离环境验证ModelDev执行/发布确认/关闭 | 按D12.6逐项标明测试素材、替代调用及未验证权限；不写Model READY |
| 真实BFF/前端未就绪 | 正式输入合同先固定；使用隔离测试装配验证真实执行与产物；可以并行编码BFF但不等待其完成 | 不伪造Gov身份，不放宽正式listener；PA05及真实页面相关断言NOT_RUN |
| 读取能力先就绪 | 真实接入基模/目标只读解析，验证身份和固定内容；登记继续独立开发 | 读取通过不等于写登记通过 |
| Model 完整就绪 | 发布正式 API；ModelDev 固定消费版本；验证真实读写、重放与失败窗口 | 对应代码/服务/数据库不替身，独立合同验收 |
| 微调正式启用 | 相应Release必需依赖、目标验证及本方案最终验收要求满足后，按原批准流程启用 | 一次提交自动完成本次微调与模型登记，不靠管理员每次补登记；不自动部署推理服务 |

公共FINETUNING仍只在本Release必需依赖及既定正式验收满足后启用。此限制不禁止D12.6的隔离候选测试；候选测试不更改Gov全局当前指针，不给普通用户开放未验收能力。Model可选装配缺失不影响原CPU；正式路径不能明知无Model还受理并承诺自动登记。

已受理后临时失联不改变原快照。到预算后保存登记失败/UNKNOWN并关闭计算；在原产物仍存在且已 CLOSED 时，用户可经当前授权触发原登记恢复。它不是新的交付模式，也不是按名字重新发现目标。

具体对接需求集中在 D08，并派生为单独的 Model 需求交接稿；**派生稿不是另一份可以独立修改的合同**。不先实现所有备选 API；优先复用已公布方法，只有不能表达必要查证时才由 Model 补一个窄接口。

<!-- source-section: D08; source-sha256: c4e44cb1d3cb0e33d452783f2d7dbf23db2dc0eadb43e08ba5c014391febd5dc -->
## D08 · Model需求交接：先留接缝，提供方就绪后接入

本节是 **ModelDev向Model提出的具体领域需求**，不是新的服务间调用规范。传输/身份/委托/超时/重放统一引用Gov合同。Model提供方先确认现有API是否足够；不足的窄能力由提供方正式发布，消费方再固定依赖。不得以本节示意方法名复制一套Model Proto。[S2]

### 8.1 本次职责与就绪条件

| 编号 | Model需要提供/保证 | ModelDev需要完成 | 正式联调证据 |
|---|---|---|---|
| MR01 可信接收 | 在已有业务mTLS入口按服务+完整RPC+用途建立可信上下文；检查tenant与资源；不信任body自报Principal | 重建metadata；只从已验证Gov委托/持久受理构造请求；不冒用Gov证书 | 无证书、错服务、越权方法、冲突tenant、伪造actor拒绝；合法原范围通过 |
| MR02 基模读取 | 返回已批准精确版本及真实size/SHA/format/加密/固定引用；短期下载始终同版本 | 冻结读取事实、临近使用才签发能力、流式核验 | 固定版本读回；源同key新版本不替换基模；禁止任意版本读取 |
| MR03 目标与名称 | Model.id与外部标识明确；目标归属/可写检查；租户内名称唯一，新建重放回原模型 | create-only与existing分支固定；预检仅提示/拒绝，不承担最终唯一性 | 同名新执行冲突；原键重放同模型；显式existing新增版本 |
| MR04 版本登记 | 原key+原业务内容幂等；完整登记内容一致性；可靠finalization | 持久原登记意图；具体写派发按D09.3与停止仲裁，不以整份意图代替在途事实 | 一次结果；同key异内容拒绝；丢响应/部分提交及停止竞争 |
| MR05 原结果查证 | 限定原tenant/业务键返回未创建、pending/部分完成、READY或冲突及原字段；READY-only旧接口含义不变 | 先查证再按合同有限重放；不拿404单独当无副作用 | 响应丢失能找回；不会创建第二版本；未完成状态不可当READY |
| MR06 固定对象与下载 | 固定版本选择器或受管不可覆写key贯穿持久引用、校验、下载和重放 | 不将versioned object降成latest；tar SHA/size与原发布一致 | 同key后来内容改变，仍下载原内容；长度/SHA不符拒绝 |
| MR07 存储交接 | 明确当前作用域可否接受原publication引用；需上传时票据不另建业务版本 | 固定一条经验证绑定；仅必要时复制，不动态切模式 | 实際从Model读取同一tar；配置缺失明确未启用 |
| MR08 删除保护 | 批准基模/登记成品的保护在实际删除路径生效；保护建立时机明确 | 在批准、引用和记录时使用权威事实；不靠隐藏按钮 | 删除与批准/登记竞争时不会删掉受保护资产 |

本版所需MR能力在当前选定绑定上真实完成，是正式启用及本方案最终验收的必要条件；**不是要求ModelDev开发人员同时实现两个仓库**。Model暂未交付时，这些接口可以先用边界替身进行消费者合同测试，但不能把替身的权限/事务结果算成MR已通过。

### 8.2 实际API优先复用

现有方法族包括 `GetModel`、`GetModelVersion`、`GetModelDownloadURL`、`CreateModel`、`CreateModelVersion`。复用这些正式API及固定模块版本；名称精确占用检查和pending查证如现有接口无法可靠表达，再由Model补一个限定能力，不新建通用查询系统。`GetUploadURL`只有当前选定交接确实需要复制时才启用。

每一项调用登记填入**最终完整RPC全名**、已签发的服务SAN、结构化上下文或既有附件字段；不能把上述用途名称当成已经发布的RPC。原actor用于审计，不独自赋权。普通查询用当前授权；受理后后台完成限于原任务批准内容，不重放长寿命用户JWT。

### 8.3 稳定键、内容与事务

| 操作/记录 | 本次规则 |
|---|---|
| 新建容器操作 | `md-container:<execution_id>`；只有new分支使用；持久保存实际Model.id收据 |
| 输出版本标签 | `exec-<execution_id>`；不是查最大版本再加一 |
| 版本登记操作 | `md-<execution_id>`；同tenant下稳定；请求追踪ID允许变化，业务key不变 |
| ModelDev原登记内容 | 原受理/目标分支、规范Model UUID（或尚待创建的原name）、基模/verify/publication关联、原tar引用/版本/size/SHA/format、加密与engine配置、交接模式 |
| Model侧内容fingerprint | 以其实际接收合同为准，覆盖影响最终模型版本的全部持久字段：tenant、目标、版本、对象固定引用、size/SHA/format、加密和engine等；不能只比key |

ModelDev的来源追溯字段不要求在Model当前Proto中无理由复制；Model回读字段覆盖自身登记内容，ModelDev另外核对本域publication/来源。未传给Model的字段不能声称由Model独立验证。

在外部首次写入前，ModelDev持久原登记意图；new分支先请求容器、记录/找回原Model UUID，再形成版本请求。**意图存在不等于这些调用都已在途；每个具体写操作取得派发资格按D09.3与Stop/deadline共同仲裁。**容器响应不明按原key查证，不按名复用；其迟到成功也不自动授权下一次版本派发。

Model先在事务外核验**不可变**对象，随后在本域事务中锁定原登记/版本，核对相同内容，插入或复用唯一Artifact，置READY并持久必须的审计事实；不把对象流和跨服务RPC包在长数据库事务中。已有历史“Artifact完成但READY未写”状态必须可恢复。同key异内容拒绝；READY重放返回原持久字段，不用新请求拼旧结果。[S4]

ModelDev查到原READY并核对确切target/version/object/size/SHA等后才写registration成功；HTTP/gRPC成功或durable ACK不等同于Model READY。是否采用已有查询或新窄方法，由Model在API交接时固定，并保留旧查询语义。

### 8.4 存储只启用当前需要的一条路径

优先核对**直接引用原publication固定对象**是否在Model的租户作用域、权限与引用格式内真实可用。兼容则直接引用，不为了格式统一重复复制。

确实不兼容时，当前环境选择**固定的受控复制绑定**：由ModelDev服务侧从原已发布对象读取，复制到Model批准范围，核对实长/SHA，并固化最终引用。upload票据只管字节传输，不能顺便生成另一个模型版本。

这两项是受管部署选择，不是每次失败时尝试的双后端。首个环境只交付它实际选择的模式；未实现模式保持不支持，不宣称已通过两套验收。选定后冻结配置，不在执行中切换模式/目标bucket。

固定VersionID或不可覆写key需被真实下载链消费；不能只在ModelDev记录版本而Model读取latest。既有上传URL未到期不能再覆盖成品；需要staging/final或固定版本的条件由提供方落到实际接口。

### 8.5 依赖不可用的结果

Model尚未配置/正式接入：原CPU可继续；FINETUNING保持未启用，模型读取与登记真实验收为BLOCKED，不新建假版本。已经受理后的短暂故障：有限查证/重放，预算耗尽保存失败或UNKNOWN并关闭计算；后续走D09的原登记恢复。

<!-- source-section: D09.3; source-sha256: c9fb381d4657923261e685fb25855f4443ec86aa663300e59b54d885609a5554 -->
### 9.3 登记具体副作用派发与停止共同仲裁

本节是v4.2.1审核R1的必要补齐。[S11] 复用本域执行/登记记录；不增加通用授权服务、许可平台或跨服务事务。**登记意图固定的是原目标和内容，不是后续所有副作用的永久派发资格。登记worker之间的lease/CAS也不能代替它与Stop共同仲裁。**

#### 9.3.1 同一执行级串行化边界

正常登记每个具体会产生外部副作用的操作，在取得本次派发资格时，必须与Stop/关闭/deadline共用同一执行记录行锁或等价数据库原子条件。事务内重新核对原执行/spec、登记阶段、业务键/内容、原目标、停止/关闭状态及剩余register/执行预算，记录该具体操作已获准在途；事务外才执行RPC或对象I/O。多实例同样受约束，禁止事务内等待网络或文件传输。

记录至少能区分“原登记意图已存”“哪项具体操作已获准派发”“其结果未明/已确认”。沿现有字段增量承载，不要求新建一套表。new分支的CreateModel、CreateModelVersion分别仲裁；若当前选定存储交接会真实写目标对象，该有限写操作也明确原key、范围及在途归属。纯只读查证和字段转换不新增派发许可记录。

每次**新的实际重发**也要检查当时Stop/deadline和原内容；不能凭停止前取得过一次许可，在停止后任意重发。正常登记只对提供方合同允许的瞬时错误有限重放，依赖原key幂等；未知结果优先查证。关闭发生后只允许原结果记录和限定只读查证，不再产生新的正常写派发。

#### 9.3.2 谁先提交，结果是什么

| 提交顺序/情况 | 必须行为 |
|---|---|
| Stop先于某具体派发提交 | 不产生该新的正常外部写调用；已有意图不豁免 |
| 仲裁时执行或register预算已到 | 拒绝派发；重放/重签/重启不重置原预算 |
| 该具体派发先获准，Stop随后提交 | 保留为原在途请求，响应可迟到；取消可取消的本地等待/I/O，原key查证并保存真实receipt |
| CreateModel已完成或迟到成功，版本派发尚未获准 | 停止已生效则不得再发CreateModelVersion；容器收据保留，不按同名自动追加或自动删除 |
| 版本写响应丢失/worker重启 | 找原操作、原内容/原目标；不换key、不用一次NotFound推定没有写入，不重训 |
| 已CLOSED后用户明确恢复原登记 | 走D09.2新的当前授权恢复命令与有限预算；仍用原内容/key，不重开计算，不被误写成永久禁恢复 |

**仲裁的是本域“是否获准派发”，不是把本域Stop与远端Model事务变成物理原子操作。**先获准的原RPC可能在Stop之后才真正到达或写成READY；必须如实记录，不能声称Stop撤回了远端写入。迟到响应本身不产生后继写操作的许可。

#### 9.3.3 在途、重放与关闭的边界

原normal handler与恢复worker共用登记记录的有限lease/CAS；仍须依赖Model的原key/内容幂等与查证，不能把lease当绝对一次副作用证明。首次派发资格丢响应、进程重启或传输结果不明时，按原记录处理；查询记录不自动重发，明确重发也必须通过相应原内容和当前时限/停止检查。

计算CLOSED不以Model READY为前置，也不因远端结果未知而编造成功。原进程/权重写者必须按既有关闭条件结束；远端Model业务结果可在关闭后继续限定查证并作为迟到事实保存。受控复制若仍有本执行服务侧实际写入者，必须取消/收齐或保持未解决，不能借“只是登记”遗漏。清理不得删除未知写入引用或唯一未发布产物。

W4与现有关闭负责人在允许真实登记写入前实现本节，并把同步竞态加入FT21/FT25；W0/W1及不调用Model的主链开发不等待该提供方上线。已CLOSED恢复不修改旧计算deadline，不扩大目标或数据，原KFP历史不改写。

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
