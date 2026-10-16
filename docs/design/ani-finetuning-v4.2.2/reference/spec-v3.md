# ANI CPU 受管微调方案 v3：单 TrainJob 与已有流程复用

更新日期：2026-10-13。状态：设计修订完成，代码待开发，负载和业务验收均未执行。

本版依据 v0.4 原始方案、P00 包及内嵌 CPU-P01、源码评审，以及后续关于数据格式、单/多 Job、复用范围的讨论，替代 v2。仓库单一权威文件为 `.scratch/finetuning-cpu/spec.md`；下载目录 v3 为本次同内容导出，v2 保留作历史版本。

本轮只更新方案，不实施业务代码、发布依赖或部署。本文中的新增字段、组件、接口及数据记录都是待实现合同；附件中的历史执行指令不构成本轮执行授权。

## 1. 一条完整微调主链

用户选择本租户 READY 数据、已批准的固定基模和微调预设，经 BFF 验证权限并持久受理。平台自动完成真实 LoRA 训练、合并导出、独立加载校验、发布和 Model 版本登记，最后关闭计算。

```text
管理员将数据放入受管 S3 → 导入并验证为 READY
                            ↓
用户提交微调 → 鉴权/目标写权 → 固定快照 → 持久受理/可靠投递
                            ↓
KFP 权威 Run → workspace-name / CreatePVC
             → prepare：准备数据与固定基模
             → train-wait：创建并等待唯一训练 TrainJob
             → export：KFP 独立组件合并 Adapter
             → verify：KFP 独立组件加载合并模型并短推理
             → collect：收集规定产物
             → publish：上传候选文件与主模型 tar
             → register：先确认发布，再登记 Model READY
             → close：关闭 KFP 及唯一 TrainJob，核验无写者
```

单 TrainJob 不表示把训练、导出和校验塞进一个 Pod 或 shell。TRAIN 在 Trainer 中运行；EXPORT、VERIFY 是两个独立 KFP 组件 Pod。三者共用锁定的微调负载镜像，各自有资源限制、日志、挂载和失败结果。KFP 负责正常步骤推进，数据库只保存业务事实，不重建 DAG。

完整业务成功必须同时具备：真实训练结果与实际优化步数；成功导出和独立 verify；远端字节核验且持久发布；原目标 ModelVersion READY、登记字段与发布归档一致；执行 CLOSED、无活跃或结果不明的计算写者。验收还要从训练卷外重新取得新包并实际使用。

202、TrainJob Complete、PUBLISHED、Model READY、CLOSED 是不同事实。正常提交后自动完成，不逐次手工建 Job、补发布或登记。当前微调尚未实现，不能继承 CPU 的运行通过结论。

## 2. 沿业务流程看复用与扩展

下表的“已有”仅指通用训练已有代码；“必要”针对本版完整 HF 模型交付目标。

| 微调流程 | 已有机制 | 新增或修改 | 为什么需要 | 是否必要 |
|---|---|---|---|---|
| 准备数据 | 固定 S3 对象导入、验证、READY | 指令型 JSON/CSV 校验 | 数值特征 CSV 无法表达微调数据合同 | 必要；复用导入，不加文件上传 |
| 用户提交 | 创建接口、预设、幂等 | 微调类型、基模/目标及参数字段 | 当前请求没有这些信息 | 必要；扩原接口 |
| 校验并固定任务 | 权限、Release、快照、持久受理 | 基模使用权、目标写权、固定引用 | 避免越权或执行中更换内容 | 必要；不单建 grant 系统 |
| 准备工作区 | PVC、固定数据读取 | 基模下载、核验、解包，微调数据映射 | 通用训练从头训练，没有基模准备 | 必要；新增业务逻辑 |
| 执行训练 | 单 TrainJob 创建、观察、停止 | 微调镜像、配方、挂载、指标适配 | MLP 与 LoRA 负载不同 | 必要；保留单作业生命周期 |
| 合并导出 | KFP 组件顺序执行 | export 组件，复用 LLaMA-Factory 导出 | Adapter 不是完整可部署模型 | 必要；不另建 TrainJob |
| 加载校验 | 字节大小/摘要核验 | verify 组件，独立加载与短推理 | 字节一致不证明模型可加载 | 必要；不做效果评测系统 |
| 收集发布 | 收集、tar、上传、对象核验 | 多级路径、HF 文件集合、入包范围、大文件 I/O | 当前只有 MLP 四文件合同 | 必要；扩现有读写 |
| 确认发布 | 当前在 close 中确认 | register 前半段执行确认 | 未确认产物不能登记 | 确认必要；不单列组件 |
| 登记模型 | Model 已有创建接口 | ModelDev 调用链、登记记录及结果查证 | 通用训练没有模型仓库闭环 | 必要；不建第二仓库 |
| 关闭与清理 | Stop、围栏、真实写者核验、UID 清理 | 纳入 export/verify Pod，保护新增资产 | 防止关闭后仍写、误删基模或成品 | 必要；不做多 TrainJob 关闭 |
| 登记失败恢复 | 幂等/查证基础 | 只重放原登记的有限恢复入口 | 不能重训或换版本掩盖丢响应 | 必要；不支持任意步骤重跑 |

v2 中的三个 TrainJob、多作业创建许可与阶段表、独立 confirm-publication 组件、授权 grant 准备/激活/查询生命周期全部移出首版。存储复制只在实际作用域不兼容时执行，不再固定要求复制。

## 3. 首版范围与职责

| 项目 | 本版决定 |
|---|---|
| 能力 | FINETUNING，CPU 单节点单进程 LoRA SFT |
| 基模 | 本租户已批准 Qwen2.5-0.5B-Instruct 的固定 ModelVersion；不支持加密、跨租户借读和任意基模 |
| 数据文件 | JSON 或指令型 CSV；源对象 ≤32 MiB，不提供本机/浏览器文件上传 |
| 数据语义 | `ani.finetune.alpaca.v1`：instruction/input/output；首个验收 Release 固定64条 |
| 用户参数 | learning_rate、max_steps；LoRA 结构、镜像、配方和资源由预设固定 |
| 执行载体 | 一个训练 TrainJob + KFP export/verify 组件 |
| 交付 | 合并 HF 未压缩 tar；Adapter 权重/配置/关联清单作为独立目录工件，符合 v0.4 D11 |
| 恢复 | 有界查证/原内容重放、关闭后的原登记恢复；不提供通用步骤重跑 |
| CPU 兼容 | 原 CPU kind、规范字节、摘要、错误字符串与拒绝集不变 |

64条是首个验收预设的固定合同，不是 LoRA 算法限制；扩大数据规模要发布经验证的新 Release，不能直接放松已冻结执行。GPU/QLoRA、分布式训练、任意用户脚本、可编辑配方、效果评测及新常驻服务均不在首版。

Gov 负责用户身份、租户映射、模块/action/资源权限、原键重放、Release绑定和可靠投递。ModelDev 负责执行、基模准备、受管组件、产物、登记调用和计算关闭。Model 负责自己的模型/版本/存储、可信接收上下文、幂等一致性及删除保护。Installer 负责 Runtime、CPU池、卷、证书、RBAC/网络和离线材料。

`contract/cpup01` 仍为共享意图/快照/Release唯一维护点。biz 仅持有领域和端口，不导入 Kratos/protobuf/存储驱动；data 实现 adapter，service/server 与 composition root 分别处理传输、身份和装配。沿用现有模块，不建设共享 ANI runtime 或通用执行器。

## 4. 用户请求、权限与持久受理

公网入口仍为 `POST /admin/v1/modeldev/executions`，扩展示例：

```json
{
  "name": "cpu-ft-smoke",
  "kind": "FINETUNING",
  "preset_id": "<微调预设 UUID>",
  "dataset_version_id": "<本租户 READY JSON 或指令型 CSV UUID>",
  "base_model_version_id": "<已批准基模版本 UUID>",
  "finetuning": {"learning_rate": "0.0001", "max_steps": 20},
  "new_model_name": "cpu-ft-demo",
  "idempotency_key": "<本次新键>"
}
```

`target_model_id/new_model_name` 条件必填且互斥；`general_parameters/finetuning` 互斥。name为1–80字符，新模型名遵守Model规则。learning_rate使用无指数规范十进制字符串，范围(0,1]，默认0.0001；max_steps为JSON整数[1,200]，默认20，规范值转为十进制整数字符串。拒绝null、重复键、未知字段、浮点截断和用户指定的镜像/命令/路径/资源。source_execution_id保留当前未开放边界。

1. Gov认证用户并转换业务租户；检查微调模块、执行创建权、数据集/基模使用权，以及新建模型或目标版本写权。
2. 对原始意图及字段存在性求intent_hash，先查幂等键。同键同内容返回原记录，不先解析最新Release；异内容冲突。
3. 新受理解析启用绑定、不可变Release、READY输入、固定基模和规范目标。外部RPC不放进本地数据库事务。
4. 事务核对binding generation，保存原operation/execution、完整快照、规范目标、授权预检事实和投递记录。并发仅一个赢家。
5. ModelDev验证Gov调用来源、tenant/operation/intent/spec，一并持久inbox后ACK。后续只用权威受理事实，不重新取可变默认值。

授权预检事实记录允许动作、规范目标、原主体审计信息及检查时间/引用；它与可信Gov来源和原受理绑定，不是独立grant或永久用户访问凭证。新的查询、下载、人工恢复按当前权限检查。正常后台完成只限于已受理原任务，不能使用用户body中的tenant/actor/target扩大范围。

## 5. 数据输入：导入已有对象，JSON/CSV共用READY

本版没有面向用户的文件上传。管理员/脚本先把文件放进批准S3范围，管理入口再导入固定对象，用户创建任务时只引用dataset_version_id；训练流程不自己调用Import导入数据。

保持旧ImportCSV的数值MLP语义。拟新增一个窄管理RPC `ImportFinetuningDataset`，接受固定object及明确format=JSON|CSV；它只处理上述Alpaca语义，不接受任意schema。相比v2的ImportAlpacaJSON，新增接口覆盖两种文件格式，内部复用原授权、持久收据、验证及READY流程。

| 格式 | 固定校验 |
|---|---|
| JSON | UTF-8数组；每行instruction/input/output均为字符串，instruction/output非空，input可空；拒绝重复/未知键 |
| CSV | UTF-8，唯一且完整的instruction/input/output表头，无多余列；标准引号转义，可包含字段内换行；按字符串读取，保留前导零和空input，拒绝畸形记录 |
| 共同 | 首例64条、源≤32MiB；固定S3版本/真实大小/SHA一致，租户和源scope均经授权 |

JSON/CSV是文件容器，Alpaca是业务语义；旧MLP的x0…x15,label文件不能直接作为微调输入。通用训练不会因为新增该接口自动支持JSON。

首次远程验证前保存原格式、schema、scope、object/version/size/SHA与request identity。状态沿用VALIDATING/READY/REJECTED：内容错误拒绝，源不可用保留原对象可恢复，成功事务提交READY。同输入ID换格式或字节必须冲突，迟到PUT不能覆盖READY内容。

规范化采用平台已批准的固定纯转换合同：保留行序与字符串内容，空input显式保留，生成规范UTF-8 Alpaca JSON。importer在READY事实保存normalization_version、实现摘要及源/派生字节SHA和大小；它不依赖用户随后选择的最新Release决定转换规则。Release声明接受的规范化版本，受理时核对一致，prepare按输入已保存的原版本重现同样字节。转换实现升级不重写旧READY事实；不支持原版本时明确拒绝受理，不在prepare偷偷换实现。规范派生文件上限64MiB，越界在READY前拒绝；源摘要与派生摘要各自保存，不能互相替代。

prepare将任一格式物化为 `inputs/data/finetune_data.json`，`dataset_info.json`在同目录固定映射ani_cpu_smoke及三列。训练只读这一锁定内部格式，CSV用户无需自行转JSON；这也是本版对已有准备步骤的增量工作，不是额外转换服务。

领域验证事实、查询format和数据库READY约束按数据类型分支；旧CSV的1024×16校验原样保留。所有请求/记录含tenant_id、租户条件查询与保持租户的复合外键。验收必须通过真实importer和PostgreSQL获得READY，不直接写库制造成功。

## 6. 基模解析、下载与安全物化

批准基模来自Model受管记录，精确绑定UUID、模型身份与artifact；展示名称不足以批准，Qwen2-0.5B不属于本例。在受理快照冻结model/version ID、provider/reference、归档SHA/size/format、未加密事实、批准/保护引用及预期HF文件集。要求READY且ref/hash/size齐全。

Gov预检使用权；ModelDev的受理前只读解析使用已验证Gov委托，执行期重签/取模从持久快照构造限定服务上下文。Model接收方核验调用服务、方法、租户、固定version及批准范围，不能从普通body tenant建立Principal。加密判断使用GetModelVersion的可靠事实，不依赖下载响应默认布尔值。

每次实际读取前调用GetModelDownloadURL签短期URL。过期可有界重签，但不得重新解析成当前版本；URL/凭据不进长期快照、日志或查询。

流式下载独立累计归档大小/SHA，对比冻结事实；先写staging，安全解包后核对HF文件及摘要，再原子发布inputs/model。拒绝绝对/穿越/反斜杠路径、软硬链接、设备/FIFO、重复成员、PAX扩展、超限数量或展开字节。首例起测归档≤2GiB、展开≤4GiB；大权重禁止io.ReadAll。

prepared-manifest绑定原数据/规范派生数据/dataset_info/基模摘要、PVC UID和spec_hash。残缺目录不当成功，重放只接受原完整字节。基模访问由服务侧adapter提供固定本次对象的短期只读能力或受控物化流；KFP组件和训练负载没有Model管理证书，也不能下载任意模型。

## 7. Release、快照和CPU兼容

新增FINETUNING、MERGED_HF_MODEL及必要输出角色，但不修改旧枚举编号。Release固定pipeline ID/version/IR SHA、Runtime内容/target、训练程序、export/verify程序、镜像digest、配方/适配程序摘要、参数/数据合同、目录/资源/预算、输出全集及登记规则。

微调快照的可选子结构保存基模与目标、JSON/CSV源格式及两种字节事实、原权限预检和受理身份、冻结export/verify组件配置、存储交接绑定。训练仍使用一个Program/TrainingPlan，不新增Trainer phase数组、阶段序号或多作业表。

| 已有契约 | 增量 |
|---|---|
| Intent | finetuning、base_model_version_id、target_model_id/new_model_name |
| InputRef | 微调schema/format、固定normalization_version/实现摘要及规范派生数据摘要/大小 |
| Program/Release | 锁定微调训练程序，ExportProgram/VerifyProgram可选项 |
| Workspace | export子路径及必要报告布局 |
| OutputContract | HF/Adapter/报告角色、in_tar、微调bundle与发布字节上限 |
| 受理Snapshot | 固定基模、目标、原授权预检、组件配置及交接绑定 |

保持现有inputs/training/reports/publication，加export即可；不新增无运行用途的configs子路径。配方固定在镜像/config中，prepare不重写配方。snapshot canonical/spec_hash在受理后不变；运行时UID、实际输出摘要和登记结果保存为运行事实。

CPU新字段必须真正不编码，不能增加零值改变规范字节。proto/codec双向映射覆盖新增结构，原CPU向量、摘要、错误和拒绝集不改测试期望。微调/通用preset kind固定；Release更新在同kind内，不能靠换kind让旧请求忽略输入结构变化。

最终Release fixture必须是完整可解析规范JSON，绑定真实锁定文件全集；设计示例不是启用凭证。API与contract依赖由提供方先正式发布，再更新消费方，不复制生成代码、不跨仓import internal或用local replace代替互操作。

## 8. 一个训练TrainJob与两个KFP计算组件

先锁定能实际运行的LLaMA-Factory/Python/PyTorch/transformers/peft、离线wheel、基础镜像和CPU配方。具体版本由真实验证决定，不在设计稿猜填。微调镜像同时包含负载适配程序和受管step入口，不能只有裸CLI。

| 执行阶段 | 程序及真实结果 |
|---|---|
| TRAIN：唯一TrainJob | finetune_entry调用llamafactory-cli train，生成真实Adapter、逐step指标、summary和Adapter关联清单 |
| EXPORT：KFP组件 | 受管入口取得冻结配置并核对训练成功/无写者后，调用llamafactory-cli export，生成完整merged及清单 |
| VERIFY：KFP组件 | 受管入口核对export真实成功后，同步执行verify_cpu.py独立加载，greedy固定短输入、最多16token，产文件摘要和报告 |

锁定sft/lora、target=all、rank8/alpha16、cutoff128、batch1/accumulation1、seed/data_seed42、use_cpu=true；关闭fp16/bf16/flash attention、额外checkpoint和外部上报。仅开放learning_rate/max_steps；准确CLI overrides由锁定版本实测写入向量。不能把MLP的参数拼法或--recipe直接传给裸LLaMA-Factory。

entry只适配固定程序，不重新实现算法、不调用Model、不持长期存储权限。TRAIN从真实日志/callback转换ani.metric.v1，summary记录实际optimizer_steps、原输入/基模/配方摘要及Adapter文件摘要，不能填预期值。受管fail配方仅用于真实失败验收，用户不可传。

CPU保留原单Training/TrainingHandle/Observation及创建许可机制；微调也只有一个TrainJob。既有request hash、确定性name、UID绑定、unknown查证和迟到创建恢复继续使用，改变的是微调程序与挂载。正常训练创建结果不明不换名重发，单次NotFound不证明无副作用。

export/verify不调用EnsureTraining，不增加TrainJob创建许可。它们通过受管step入口核验TokenReview、权威Run、真实task/Pod UID、原spec、stop/deadline及前序结果，再同步运行固定命令。服务白名单、PipelineStep及runtimeproof必须支持export/verify/register，但不能开放任意步骤名或命令。

KFP计算步骤禁cache和自动计算重试，Pod restartPolicy=Never，入口不会在查询重放时再次启动CLI。复用既有task/Pod身份约束；为export/verify持久保存最小执行事实（role、原task/Pod UID、启动/结果及清单引用），保证一次绑定、结果不明查证原Pod。此记录服务于已有受管步骤及关闭，不建设通用步骤调度器或多Job许可账本。

入口同步等待子进程，把取消/SIGTERM传给进程组并收齐退出事实，禁止后台遗留写者。前序成功由后继组件/owner结合真实Pod主容器零退出、KFP终态、控制器状态和文件清单独立核验，不只相信自报成功。训练结果确认后才导出，导出结果确认后才校验。

TRAIN的prepared-manifest、adapter-manifest及EXPORT的merged-manifest在前序实际完成后形成并确认；实际输出摘要属于运行事实，不预填受理快照。最后collect前已能证明文件属于原执行和前序，不需要多阶段TrainingPlan。

## 9. 路径、挂载与文件安全

微调RequiredFiles相对workspace根；微调collect/publish从/workspace定位，CPU仍用原training根和叶文件语义。

| 路径 | 内容 |
|---|---|
| inputs/data | 固定派生finetune_data.json、dataset_info.json |
| inputs/model | 已核验基模HF目录 |
| training/adapter | 训练Adapter权重和配置 |
| export/merged | 合并HF全文件集 |
| reports/train | metrics、summary、adapter-manifest |
| reports/export | merged-manifest及导出诊断，不能混入HF模型目录 |
| reports/verify | verify.json与该阶段诊断 |
| publication | collected inventory、candidate等非权威暂存 |

| Pod | 挂载原则 |
|---|---|
| prepare | inputs及准备报告可写，训练/导出目录不写 |
| TRAIN TrainJob | inputs只读；training和reports/train可写 |
| train-wait | 训练产物/清单只读，不能写权重 |
| export组件 | inputs/training/reports/train只读；export和reports/export可写 |
| verify组件 | export/reports/export只读；reports/verify可写 |
| collect/publish | 产物目录只读；publication及受限spool可写 |
| register/close | 只读必要报告/候选，不持Model管理凭据，不写训练产物 |

Trainer明确构造训练Pod挂载；KFP明确构造export/verify挂载，不能假定挂载自动跨系统传递。Runtime保持单节点与单trainer target。CPU池selector/toleration分别落实到Runtime和组件，use_cpu本身不证明节点调度。

使用UID/GID10001、只读根文件系统和受管可写卷/缓存目录。所有子路径校验非根、非重叠、规范、无穿越和反斜杠；新增export同步进WorkspaceBinding、wire及owner proof。逐级目录fd+O_NOFOLLOW/O_DIRECTORY，叶文件拒绝链接/FIFO/设备/多链接及读期间变更。prepare/cache/spool的可写范围固定，不让用户传环境变量或路径。

## 10. 收集、tar、上传与登记前确认

最终RequiredFiles根据锁定镜像与固定基模的真实导出全集逐项固定；不能把猜测的七个HF文件称为完整枚举。至少包含config/tokenizer/完整safetensors（需要时含shards/index）、Adapter权重/配置/关联清单、真实metrics/summary、verify报告。export实际缺/多文件均失败，升级导出集走新Release。

主模型未压缩USTAR tar，仅包含in_tar=true的export/merged文件。成员名严格剥离此前缀，保留必要内部层级，根不套merged/export目录；拒绝碰撞、link、穿越、PAX、非法类型和尾随数据。output-manifest、Adapter和平台报告不进入主tar。Adapter为独立目录工件，绑定原基模版本。

文件SHA、manifest SHA、bundle SHA独立；Model checksum使用整个tar字节摘要。CPU旧tar成员规则与manifest行为不变。max_total_bytes继续表示collected files累计字节，微调起测4GiB；新增微调bundle≤3GiB、总发布字节≤8GiB，双份合并文件+tar计入磁盘和传输预算。

publish流式捕获/校验到受管磁盘spool，上传固定执行前缀，返回candidate；不自证上传Pod已退出，不直接宣称PUBLISHED。全部对象绑定固定版本或受管不可覆写引用。

register的前半段调用复用的发布核验：检查原publish task/Pod UID真实成功退出、固定远端对象实长/SHA、manifest与tar合同，事务持久publication后才进行Model登记。微调ReportStepResult/可信proof允许register执行这次确认；CPU仍由close确认。lora-sft逻辑key要同步到全部可信核验层。

不单列confirm-publication组件。确认失败不调用Model，保留产物并关闭计算；重复确认返回同内容原事实，不能改变发布对象。

## 11. Model授权、存储交接与可靠登记

新增的是ModelDev→Model业务调用链，复用ModelService现有进程/listener与公开API；不造第二仓库或新增常驻服务。双方遵守Gov的服务间调用规范，composition root管理地址、CA、证书、私钥、字面server_name和正超时。

mTLS TLS1.3、精确DNS SAN、完整方法白名单、可信租户和资源范围必须同时成立。Model要装配真实resolver，不能只加ModelDev证书允许清单而保留nil Principal，也不能启用ANI_ALLOW_DIRECT_ACCESS、明文fallback或InsecureSkipVerify。

首版用专用结构化上下文承接现有服务委托合同，不新建grant准备/激活/查询生命周期。受理前解析和受理后执行分别登记，避免“先解析才能生成spec，却要求先有spec”的循环：

- 受理前只读解析上下文来自已验证Gov来源，绑定可信tenant、intent_hash、唯一base_model_version_id及使用权预检；不要求尚未生成的spec_hash，只允许固定基模/目标元数据解析，不开放创建、上传、登记或任意资源读取。
- 受理后执行上下文包含原tenant/operation/execution/intent/spec、规范目标/分支、固定基模和原授权预检引用；ModelDev只从可信Gov来源的持久inbox构造，用于固定输入读取或限定后台登记。

KFP、训练程序和公网body均不能提供或修改这些身份事实；接收方按实际调用服务与上下文用途区分允许的方法，不能将解析权限扩大为写权限。

Model把已验证调用服务与该方法登记的“原受理限定委托/后台产物登记”范围结合后建立可信上下文，核对附件字段一致、租户映射、目标归属/可写状态、基模批准范围及允许动作。仅actor字段或同CA证书不证明用户授权；普通body tenant只作一致性选择，不独立建立Principal。接收方信任的事实提供方与校验必须在双方合同中显式登记，负向拒绝伪造/冲突附件；原actor只用于审计，不伪装成当前用户。

| API/能力 | 本版使用方式 |
|---|---|
| GetModelVersion/GetModelDownloadURL | 解析并读取唯一已批准固定基模，签名访问能力短期化 |
| CreateModel | new_model_name分支用独立稳定容器key，同内容响应丢失可找回原UUID |
| CreateModelVersion | 原目标、原version/key、固定tar引用及全fingerprint，恢复原结果 |
| GetUploadURL | 只有需要受控复制时使用；票据不另创建业务版本 |
| 登记查证 | 优先扩既有受限管理查询，按可信tenant+原key返回pending/ready及原字段；旧READY-only调用默认语义保留 |

当前查询若无法满足pending/部分提交查证，只补一个登记领域限定查询方法；不是另建通用授权或查询框架。接收方可信上下文、对象实长/摘要核验和finalization修复属于必须的跨仓前置，不允许用fake Model的权限/数据库替代。

存储交接使用冻结受管绑定确定：若Model能够读到并接受publication的同一不可变对象版本、同一租户作用域，直接映射storage_path；不能仅凭endpoint相同推断兼容。若不同，则由ModelDev服务侧publisher有限复制到Model管理作用域，再独立核验实长和SHA，固定交接记录。两种方式都不给KFP或训练Pod管理/目标存储凭据。

复制使用稳定upload_key与staging/final对象引用；过期只重签原票据。最终引用必须不可覆写或显式版本固定，不能让旧签名PUT覆盖已登记成品。绑定在受理前验证，执行中不能临时切模式或存储位置；缺少兼容绑定就明确不可用。

直接引用和复制所得的固定版本必须贯穿Model持久artifact/reference及完整fingerprint，checksum核验和GetModelDownloadURL均读取同一版本；不能把版本化publication映射成读取latest的裸key。若Model adapter不能表达版本选择器，只允许受管不可覆写key；两种不可变性均无法证明时拒绝登记。READY重放也不能丢弃版本选择器。

新模型容器key固定md-container:<execution_id>；版本固定exec-<execution_id>；registration_key固定md-<execution_id>。不查最大版本加一，不换key修复未知响应。登记记录冻结原目标、原授权/受理、基模/verify/publication摘要、交接方式、tar引用/版本、大小/SHA/format、加密和engine字段及全fingerprint，重放只用原内容。

Model核验不可变对象后，以事务锁定原登记/版本，核验或插入唯一同内容artifact、置READY并写审计。artifact已提交而READY未完成的窗口必须可恢复；同key异内容冲突，READY重放返回原持久真值，不能用新请求字段拼旧结果。ModelDev查证READY并逐字段对比原记录后才持久登记成功。

缺配置时只允许明确禁用微调或对其必需配置启动失败，不让CPU伪造成功。正式跨仓API先由提供方发布验证，再固定消费方版本；不复制Proto或直接写对方数据库。

## 12. 状态、失败与有限登记恢复

沿用compute/delivery/close/resource维度与current_step，增量显示export/verify结果、registration状态与有界错误，不建设第二套任务状态引擎。compute SUCCEEDED表示唯一训练作业真实成功，不等于微调完整交付；export/verify失败必须显示具体步骤失败且不得登记READY，不能只突出训练成功。

delivery PUBLISHED只表示已发布事实；登记失败不抹除它。完整成功必须verify通过、publication存在、registration READY和CLOSED同时满足。registration至少区分未发起、进行中、UNKNOWN、失败、READY；保存原请求/receipt，不能从HTTP200推断业务成功。

| 失败 | 必须行为 | 恢复边界 |
|---|---|---|
| 数据/基模/目标权限失败 | 受理前拒绝，无计算 | 修正后新请求 |
| 下载URL过期/源暂不可用 | 原字节有界重签/重读 | 超预算失败关闭，不修改快照 |
| TrainJob创建不明 | 保存原permit/name/hash查证 | 不重新POST或替换资源 |
| 训练/export/verify失败 | 不执行后续交付，保存诊断，关闭计算 | 修正后新执行，不重跑原计算步骤 |
| collect/publish/确认失败 | 保留唯一输出，关闭计算 | 不承诺常规补发布接口；受控例外另做预检 |
| 登记失败/响应丢失 | 保存原key/内容，先查证或有限重放 | 计算关闭后允许原登记恢复 |
| 登记异内容冲突 | 停止自动重放，明确待处理 | 不改目标/version/key |
| stop/deadline/重启 | fence后阻断新步骤，停止并查证原资源 | 不明确则NEEDS_REVIEW |

拟新增POST /admin/v1/modeldev/executions/{id}:register，Gov先检查当前目标写权及执行归属并持久恢复命令；ModelDev用自己的原登记事实执行。仅接受训练成功、verify通过、已确认publication、CLOSED且无关闭待核实的执行。它不运行旧KFP步骤、不挂训练卷、不开放TrainJob许可，只使用原目标/字节/key。

正常登记和恢复共享原记录，有限lease/CAS防双发；恢复有自己的RPC正超时，但不修改旧计算deadline。正常stop/deadline禁止新的登记副作用；已在途写入不能当没发生，继续原key查证。CLOSED可如实保存迟到receipt；用户主动原登记恢复是另一次当前权限校验的明确请求。

## 13. 停止、deadline、关闭与资产保留

先持久stop/close fence，阻断后续计算组件和正常登记；停止KFP及唯一TrainJob，核验其历史JobSet/Job/Pod写者和全部KFP写者（含export/verify）已结束。failure finalizer与独立CloseWorker走相同证据路径，关闭不等待Model登记成功。

没有多TrainJob列表/阶段创建许可。需要扩的是KFP步骤身份、控制器历史与新组件写者证明：配置回调通过之后发生Stop的Pod仍必须被停止或等待退出；不能因“曾授权”遗漏已在途组件。未收到结果时查证真实Pod，不通过重启负载伪造完成。

KFP Failed、当前Pod不存在、单次TrainJob NotFound都不足以CLOSED。历史executor节点从未落地需要控制器独立证据；曾落地而现在缺失的Pod仍按未知处理。现有deadline卡CLOSING缺口作为共享关闭修复，以真实Argo反例证明判定，不删除安全检查凑结果。

close只关闭计算，不自动删PVC。UID plan/apply及经授权保留策略管理清理；活跃/未知写者、未确认创建、关闭待核实、唯一未发布输出都阻断。发布失败保留输出；成功后的卷清理不影响S3工件独立下载。

基模、READY输入、登记成品要有有限保护记录或引用检查，删除入口实际阻断；没有安全解除合同就先禁删受保护资产，不建设通用引用图。保护和READY/登记事实一致提交，禁止仅靠操作手册提醒。

## 14. 起测资源与一致时限

资源是待验证预算，不是已证实峰值。execution_timeout_seconds=10800，受理时固定deadline_at；正常路径预算总和10800秒。

| 环节 | CPU request/limit | 内存 request/limit | 预算 |
|---|---|---|---:|
| 排队/编排余量 | — | — | 900s |
| prepare | 2/4 | 6Gi/8Gi | 900s |
| TRAIN及train-wait | 训练2/4，等待0.5/1 | 训练6Gi/8Gi，等待512Mi/1Gi | 3600s |
| export组件 | 2/4 | 6Gi/8Gi | 1800s |
| verify组件 | 2/4 | 6Gi/8Gi | 900s |
| collect | 1/2 | 1Gi/2Gi | 600s |
| publish | 1/2 | 1Gi/2Gi | 900s |
| register（含发布确认/必要交接） | 组件0.5/1，服务侧I/O单独计入 | 组件512Mi/1Gi，服务侧流式内存 | 600s |
| 正常close余量 | 0.5/1 | 512Mi/1Gi | 600s |

每步有效截止min(步骤开始+固定预算, execution.deadline_at)，训练负载和等待共用TRAIN预算。RPC继承取消与剩余时限，重试/重签/重启不续期。微调Runner需要按步骤剩余预算执行，不能沿用统一1800s隐含截断。计算入口退出时收齐子进程，不能只给等待请求超时。

deadline后禁止新计算和正常登记，关闭worker仍用独立清理时限完成安全查证；这不延长计算许可。600s是关闭目标，证据不明则待核实，不直接CLOSED。

工作区起测20GiB。publish磁盘spool≤8GiB，临时磁盘request10GiB/limit12GiB，不用内存emptyDir装整模；export/verify的受管缓存/临时卷和服务侧必要复制分别计入配额。所有基模/权重hash、copy、tar流式处理。

M0验证20steps，M2验证max_steps上界及正常/失败预算后才启用对应Release；预算不足发布新已验证Release，不改在途任务。多租户实际调度继续遵守现有绑定能力边界，不能因本版设计声称任意多租户运行已接通。

## 15. 实施顺序与必须代码落点

先交付真实最小纵向行为，不先造完扩展框架。能并行的工作限定文件owner；M1是同一个集成门槛。

| 里程碑 | 同步交付 | 可观察结果 |
|---|---|---|
| M0固定真实负载 | 锁定镜像、两个源格式与规范化向量、LoRA配方、entry/export/verify及完整文件集 | Fedora真实20step→合并→独立加载；只证明负载 |
| M1第一条业务主链 | 真实数据READY、Gov鉴权受理、单TrainJob、KFP export/verify、确认并登记、关闭 | 真实Gov/ModelDev/Model业务代码和Postgres；外部KFP/K8s/S3可用明确边界替身 |
| M2必要故障与兼容 | 创建响应丢失、stop/deadline、组件取消、重启、Model部分提交/丢响应、原登记恢复 | 原ID无双计算/双版本；真实关闭或待核实，CPU相关回归通过 |
| M3目标环境 | 正式API/镜像/Release，实际证书/Runtime/卷/网络/存储绑定 | 真实KFP/Trainer/S3闭环、Model READY、独立取包和Inference调用 |
| M4独立交付 | 操作、Release切换回退、有限恢复和清理保留、证据索引 | 非原开发者复演，完整v0.4范围逐项验收 |

M1若使用控制面边界替身，必须驱动真实训练、导出和校验程序并投影实际进程终态，不能合成权重/清单或直接返回SUCCEEDED。Gov→ModelDev→Model权限和持久化不替身。替身闭环与目标集群验收分别报告。

| 范围 | 必需入口与改动 |
|---|---|
| contract/wire | intent/snapshot/release/output_manifest、codec/conformance；新增微调字段、数据语义、export/verify/register步骤，CPU字节不变 |
| 输入READY | managed_input/input_importer/managed_materials、data/input、objectstore验证、管理RPC与迁移；新增ImportFinetuningDataset，不改旧ImportCSV |
| 受理/配置 | admission_resolution、service/materials、Gov公网合同/模块/目标写权/快照/投递，冻结程序和存储绑定 |
| 单TrainJob | Trainer程序/挂载安全检查；保留原单Training/UID/permit和unknown恢复，不新增phase表 |
| KFP计算组件 | component/runner、step入口与微调镜像、service/step_runtime、runtimeproof/tasks/proof/owner_close；role/Pod结果记录、进程取消与新写者证明 |
| 文件/发布 | workspace/collector/pipeline_io、objectstore/output_verifier；workspace根、多级安全路径、HF tar/大文件、register确认发布 |
| Model闭环 | biz ports、data/model client/resolver/publisher（必要复制）、登记repository和装配；Model可信resolver/受理附件、pending查证、finalization及删除保护 |
| 查询/恢复 | 当前步骤、export/verify/registration结果、原登记命令；Gov当前权限，CLOSED后不重开计算 |

proto按固定流程生成，提供方API先发布并验证版本/SHA再更新消费方。仅跑改动对应行为测试，有实际影响才扩大；提交前遵守仓库make verify要求。文档修订本身不触发训练/全量构建或部署。

## 16. 验收范围、未来扩展与来源

| 验收 | 必需观察 | v0.4映射 |
|---|---|---|
| JSON/CSV READY | 原对象固定、规范派生摘要一致、前导零/空input/引号换行正确，混用MLP数据拒绝 | C06–C08 |
| 权限/受理/Release | 无权目标和跨租户基模在受理前拒绝，可信上下文冲突拒绝，原键不变、切Release不改在途 | C01/C02/C09–C11 |
| 单Job真实微调 | 一个TrainJob UID，实际optimizer_steps/Adapter；export/verify是两个受管KFP组件且前序真实终态 | C13/C14/C17 |
| 文件安全/加载 | 损坏归档、穿越/link、缺/多HF文件、目录symlink均拒绝，verify失败不得登记 | C18/C19 |
| 确认登记 | publish未退出不可确认，远端实长/SHA/tar一致，register先持久发布后Model READY | C19/C20 |
| Model一致性/恢复 | 新建响应丢失、artifact写后中断、READY响应丢失；原key唯一真值、异内容冲突 | C21/C22 |
| 存储交接 | 兼容绑定直接引用，无多余复制；不兼容才复制，原对象/交接对象均固定且摘要一致 | C19/C20/C22 |
| 停止/重启/deadline | 单TrainJob及全部KFP写者关闭，含export/verify子进程；unknown/未落地节点安全处理 | C24–C26 |
| 原登记恢复 | 当前权限、原目标/产物/key；无需训练卷、不重开Trainer或KFP计算 | C21/C25/C26 |
| 保留/删除/清理 | 唯一未发布输出不GC；基模/READY输入/成品受保护；精确UID plan/apply | C28–C30 |
| 独立新HF使用 | 不挂训练卷，经授权取包并加载，Inference实际调用新包 | C23 |
| 自动完成/CPU兼容 | 提交后全链自动完成，预算实测，非原开发者复演；旧字节/错误/拒绝集与相关CPU行为不变 | C16/C31–C34 |

完整C01–C34范围不因本表重点映射而缩减。GPU/QLoRA、效果评测等标DEFERRED，不作PASS。只保留恢复/复现所需仓库SHA、API/镜像/Release摘要、cluster/context/namespace UID、operation/execution/Run/TrainJob及组件Pod UID、对象/manifest/tar摘要、Model receipt、关闭和独立调用结果，凭据/有效签名URL不入记录。

将来有明确需求时可改为多TrainJob，但首版不预建其框架。保持train/export/verify独立业务边界、固定产物合同和Release/PipelineVersion即可；届时增加阶段创建/持久化/关闭能力，并发布新Release供新任务使用。已受理任务按原快照完成，用户接口可保持一致；现有单作业记录不原地改成三作业。

本版状态为DESIGN_UPDATED；负载、边界替身业务链、目标环境和最终v0.4验收均NOT_RUN。

来源：用户提供的v0.4 source/architecture、development、operations及cpu-baseline；P00及内嵌CPU-P01；原finetuning-cpu、v2及本次讨论。关于CSV容器的外部依据为LLaMA-Factory官方[data说明](https://github.com/hiyouga/LLaMA-Factory/blob/main/data/README.md)；实际锁定版本仍在M0验证，本版统一物化内部JSON不依赖任意CSV类型自动推断。

本次本地源码核对：ModelDev HEAD `5b7fd05d9a74a8d0c65577e71b9a5c2db5dc8163`，本次origin/main缓存 `c5fd93c60f4aa6af229ef39d3cc503396f6d0ffb`；Gov HEAD `3f49ebc2aec9f83ede9dba200f3fb5a2edd53bf5`；Model本地HEAD `6473f1ec1386067e68dbd1294c10c660da7d1e7b`，此前接收方评审另核对过远端master `71c10de`。实施前固定相关工作树、正式依赖和实际部署版本，不将静态方案或历史记录当成本轮运行验收。

| v2决定 | v3收敛结果 |
|---|---|
| 三个TrainJob/phase表/阶段permit | 一个训练TrainJob，export/verify为KFP组件，保留必要task/Pod事实 |
| 只接受JSON | JSON与指令型CSV导入，共用READY与固定内部数据映射 |
| ImportAlpacaJSON | 一个微调专用ImportFinetuningDataset覆盖两种容器，旧ImportCSV保持MLP语义 |
| grant准备/激活/查询 | 复用可信服务上下文与原受理事实，不新建grant生命周期 |
| 独立confirm-publication | register先确认发布再登记，CPU仍由close确认 |
| 无条件复制到Model存储 | 冻结兼容绑定，兼容直接引用，否则受控复制 |
| 独立无用途configs子路径 | 保留原布局，仅增加必要export；镜像/config为唯一配方来源 |
| 未突出已有/扩展范围 | §2按完整业务流程解释已有、增加部分、原因及必要性 |
