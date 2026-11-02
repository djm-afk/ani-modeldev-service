# EN02 25秒总预算耗尽后续跑：NOT_RUN

- 当前状态：仅有未提交测试草稿 `internal/data/tenantenvironment/budget_recovery_test.go`；不是PASS证据。
- 本地草稿SHA256：`4538c8935df0bec9c29cc9250634fb1128ea189b0f574b7bae6255448ddee7b0`。
- 初次传输命令：`rsync -a internal/data/tenantenvironment/budget_recovery_test.go fedora:/home/chabking/tenantenv-20261101/modeldev-environment/internal/data/tenantenvironment/`。会话 `17571` 无输出且尚未完成；没有取得传输exit，不能确认远端文件与本地相同。
- Fedora格式化：NOT_RUN；未发出 `gofmt` 命令。本地没有格式化或执行测试。
- Fedora源码SHA：NOT_RUN，未读取，当前值为null。
- 计划定向命令（未执行）：`GOTOOLCHAIN=auto go test ./internal/data/tenantenvironment -run '^TestEnvironmentPostgresEnsureBudgetExhaustionResumesReceipts$' -count=1 -v`。测试exit为null，没有测试输出。
- 阻断事实：负责入口诊断的 `/root/resource` 已报告独立有界SSH禁用连接复用后exit255，`Connection timed out during banner exchange`，目标 `172.16.101.31:22`。这是入口诊断结果，不能当成本测试的exit。
- 草稿预期行为：使用生产Ensure/Repository/Platform和真实PostgreSQL，仅对Kubernetes外部RoleBinding GET施加每次4秒、尊重context的延迟；消耗生产固定25秒总预算后，核对已落盘PROVISIONING/RESOURCES及typed receipts、锁已释放、未提前暴露READY；取消外部延迟后显式Ensure继续同登记、原Namespace和对象UID直到seal/投影/CAS READY，Experiment仅创建一次。该行为仍待实际执行核验。
- 恢复后只接续原传输并在专用Fedora目录远端格式化、执行上述必要定向测试及核对local/Fedora SHA。入口恢复探测由resource负责；本项未重复连接，也未使用本地fallback。
- 既有正式源码提交仍为 `d807cbdbd5dd6664d61480acedc752e19cfca48a`。未更改生产逻辑、预算、API/生成文件、依赖或 `evidence/modeldev-tested-source-identity.json`。既有丢响应/SIGKILL恢复及d807门禁PASS覆盖保持原界限，不包含本次未运行草稿。

结构化记录：`evidence/03-environment-budget-recovery-record.json`。

## 第二轮已解除

Fedora入口恢复后，原传输handle17571已不存在，且先前从未启动测试。本轮将同一草稿同步到已核对的最终源码mirror，在Fedora执行gofmt和唯一一次定向测试：exit0，25.555s；固定Ensure总预算实际25.001233984s，6个完成请求/1取消，7条持久receipt（6confirmed/1pending），锁释放，显式续跑保持原environment/Namespace/对象UID，到sealed READY且Experiment POST=1。当前权威结果见03-environment-budget-recovery-record.json和03-environment-budget-recovery.log；本文前述NOT_RUN仅为上一轮历史状态。
