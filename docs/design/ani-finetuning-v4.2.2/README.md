# ANI CPU受管微调 v4.2.2 完整计划包

平台基线仍v0.4；直接底稿v4.2.1。采用两轮讨论：R1具体登记派发与停止共同仲裁，R2真实BFF验收后置，R3组合/默认黄金向量。当前先完成真实执行与产物主链；最终BFF、前端、Model、独立Inference及原参数范围不删。GPU独立准备不阻塞CPU。

## 阅读顺序

1. 打开`index.html`，当前实施优先阅读`ANI-CPU受管微调-v4.2.2-主流程开发与分段验收手册.html`。
2. 权威`spec.md`先看A12、D11.6、D12.6：当前做什么、到哪里验收、不经过BFF怎样隔离测试。
3. 再看`tasks/index.md`和对应W任务。W1先定合同/解析，W3/W6先主链；W1-P4/W6-P2为后续BFF工作。
4. 登记实现前读D09.3；参数组合读D02.6.1；真实BFF开始前再读D12.7。

## 文件

| 文件 | 用途 |
|---|---|
| ANI-CPU受管微调-v4.2.2-业务与流程方案.html | A01–A12业务、范围、对象、流程与阶段 |
| ANI-CPU受管微调-v4.2.2-开发与Model对接说明.html | D01–D14开发与Model接缝 |
| ANI-CPU受管微调-v4.2.2-主流程开发与分段验收手册.html、guides/core-flow-first.md | 从权威正文切片的当前开工手册 |
| model-integration-requirements.md | MR需求及原登记派发仲裁 |
| parameter-and-frontend-contract.md | 合同先行、前端后接的同源视图 |
| gpu-extension-boundary.md | 原GPU独立准备，不新增当前前置 |
| tasks/W0.md–W6.md | 原七工作包的当前与后续交付 |
| records/acceptance-plan.json | 32FT、15细化（原12+新3）、10PA、34原映射，分阶段记录 |
| records/*.template.json | 另存实际进度/主链/竞态/BFF候选/参数与结果；不填假证据 |
| reference/spec-v4.2.1.md | 1375行未改动底稿；更早reference保留 |
| CHANGELOG.md、review/spec-v4.2.1-to-v4.2.2.diff | 本轮完整修订与差异 |
| review/*-checks.json | 仅文档静态/浏览器检查，不是业务验收 |

## 采用位置与进度保护

保持包目录，不只复制一个HTML。先保存旧v4.2.1及已填任务进度，再审阅本版。实际权威路径沿用`<实际ani-modeldev-service工作树>/.scratch/finetuning-cpu/spec.md`；若已迁ANI-doc，以已登记唯一位置为准。不要双写主方案，不以本包强制`git add -f` scratch。

本包权威正文SHA256：`5323612099bbfe54f2e13b3aea4a5c3c4f9a4cf143a64f7cc526b530f9925ec9`。采用记录包含实际绝对路径、版本/SHA与仓库commit；本包没有给出假Fedora路径、BFF地址或集群API端点。旧CPU-P01/ENV职责不变，不把历史所有goal重新拼接执行。本包不是新的全平台总控。

## 机器职责

本机阅读、编码、文档、Git与受控转运；既有SSH别名`fedora`上的批准目录负责生成/编译/模块测试/镜像与Pipeline/真实负载；.10～.12所属K8s执行真实ModelDev/KFP/Trainer和集成。不要在节点宿主机改代码、构建或裸跑服务。

## 记录口径

WORKLOAD、CORE_FLOW、MODEL_INTEGRATION、BFF_INTEGRATION、SCHEME_FINAL、GPU_PRECHECK是测试记录scope，不是运行API字段/授权能力。当前不等BFF，后置不等于DEFERRED；Model没登记不能标A10单次成功；同一FT/PA拆断言记录，局部PASS不覆盖未验证的上游权限。

原验收计划全部NOT_RUN。旧的实际结果单独保留并评估可否复用，不自动迁移为新PASS。当前只做文档，不代表计划中任何命令已存在或已获远程写入授权。

## 文档重建

只编辑spec.md。项目侧按原约定在Fedora使用`tools/requirements-document-tools.txt`中的文档依赖，运行`tools/render_package.py`，随后`tools/check_package.py`、`tools/check_browser.py`，最终`tools/seal_package.py`。本包工具只处理文档，不编译/部署业务。浏览器检查需要已有Playwright和可用Chromium，缺少则报告NOT_RUN。工具通过set_content检查本地HTML呈现/页内操作，不宣称跨页导航通过；链接目标另做静态检查。本次环境的导航限制和未测项见review/browser-checks.json，不将环境失败改成PASS。

计划/模板有修改时生成器会拒绝覆盖，先另存实际结果再比较更新；不要靠删除已填记录使检查通过。更新self-review后重新派生HTML并重复检查。manifest不包含自身，SHA256SUMS覆盖payload与manifest；整包外部SHA另列。
