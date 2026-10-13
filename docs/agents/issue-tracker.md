# 任务跟踪：本地 Markdown

任务、需求规格和待决策问题保存在 `.scratch/<feature-slug>/`。技能要求发布、读取或更新任务时，直接操作本地文件；本配置不使用远端 Issues 作为任务跟踪器。

`.scratch/` 已由根目录 `.gitignore` 忽略，保留任务过程和现场证据。长期合同和已接受的决策整理到正式文档；凭据和恢复材料沿用既有私有目录边界。

## 普通任务

- 每个功能一个目录；规格为 `spec.md`，任务为 `issues/<NN>-<slug>.md`，从 `01` 编号，每项任务独立保存。
- 顶部 `Status:` 使用[分诊状态](triage-labels.md)，表示任务准备程度；`Resolution: open`、`resolved` 或 `wontfix` 单独表示处理结果，初始为 `open`。
- `Blocked by:` 列出同功能目录内的前置任务编号，没有依赖时写 `None`。只有前置任务实际满足验收并设为 `Resolution: resolved`，才解除依赖；`wontfix` 需要重新确定范围或依赖。
- 评论和讨论追加到 `## Comments`；实际完成后，在 `## Result` 写明变更、具名验证、证据位置和未验证范围，再设为 `Resolution: resolved`。
- 不处理的任务写明原因，设为 `Status: wontfix` 与 `Resolution: wontfix`。关闭不处理不代表实现完成。

技能生成的 `ready-for-agent` 任务表示可以接手，不代表已经交付，也不扩大修改、数据库操作、提交、推送或部署授权。规格仅在完整范围实际完成后结项。

读取用户指定路径或编号；编号属于所在功能目录。只有编号且无法从当前任务确定功能时，先询问功能名称或文件路径。

## Wayfinder 决策任务

决策探索与普通任务采用独立生命周期：

- 总览为 `.scratch/<effort>/map.md`，保存 `Destination`、`Notes`、`Decisions so far`、`Not yet specified`、`Out of scope`，用 `Labels: wayfinder:map` 标识。
- 决策票据为 `issues/NN-<slug>.md`，从 `01` 编号；`Type:` 为 `research`、`prototype`、`grilling` 或 `task`，对应 `Labels: wayfinder:<type>`，问题写在 `## Question`。
- `Status:` 为 `open`、`claimed` 或 `resolved`；这类票据不使用普通任务的分诊状态或 `Resolution:`。
- `Blocked by:` 指向同功能内的前置决策；依赖均为 `resolved` 后，按编号选择首个可执行的 `open` 票据，先记录 `Status: claimed` 和 `Claimed by:` 再开展工作。
- 结论写入 `## Answer` 后设为 `resolved`，在地图中追加摘要和以票据标题命名的链接。决策解决不代表业务实现或真实环境验收完成。

领取任务前重读当前文件，并行工作遵守根目录 `AGENTS.md` 的职责与文件边界。
