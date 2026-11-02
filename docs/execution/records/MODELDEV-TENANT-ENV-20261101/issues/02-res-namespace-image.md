# 02 Res 统一 Namespace/UID 与动态镜像授权

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

落实各 Res owner 对同一标准 Namespace 的精确读取，并将 ModelDev 镜像 Secret 授权改为受信动态目标。
依据：[spec.md](../spec.md) §2、§7.5、§12.1–12.4、§14.3；本项实现代码和维护工具，现场回填与切换由09执行。

## Scope

- 仅 ani-resource-service；新增 internal/tenantnamespace/identity.go；配置 source proto、validate 与 cmd wiring。
- Network：internal/biz/network/{vpc,network,subnet}.go；internal/data/network/{postgres,kc,vm_namespace}.go、queries/vpcs.sql。
- Sandbox：internal/data/sandbox/namespace.go、internal/service/sandbox/service.go；Storage：internal/biz/storage/{types,validate,usecase}.go、internal/data/storage/k8s.go。
- VM/Instance：internal/data/vm/environment.go、internal/biz/instance/types.go、internal/data/instance/network.go。
- Image：internal/biz/image/{pullsecret,pullsecret_caller}.go、internal/data/image/pullsecret.go；新增顺序 migration、一次性回填命令及必要生成物。

## Implementation

1. 实现纯 helper：严格 canonical 非零 resource UUID、固定 tenant-UUID、两项 network 标签、Active/无删除/非空 UID；不持有业务 DB或发 RPC。
2. 删除可配置 namespace_prefix，reserve 原字段号/名字并拒绝旧非空配置；ParseTenant 必须比较原字符串与 UUID.String()。
3. CreateVPC/CreateSubnet 在事务外精确 GET NamespaceIdentity；原接纳事务插入或比较 tenant/cluster/name/UID，禁止冲突覆盖认领。
4. 新增下一顺序 migration：namespace_uid nullable text，并 CHECK 为 NULL 或 trim 后非空；新写必须非空，runtime 遇 NULL 硬失败。
5. 实现只读计划/精确条件回填工具：仅既有行、标准名称和 live归属正确、仅更新 namespace_uid IS NULL；已有 UID 不覆盖。
6. Network KCProvider 去掉404自动创建；namespaced provider操作先比原 Namespace UID，再保留 provider UID/owner护栏。
7. Sandbox删除 Ensure/ensure bool自动创建路径，ResolveCreateSpec只读既有 Namespace并冻结原 UID。
8. Storage DTO补 UID/Phase，CreateVolume replay亦需验证；写前复核本次 live UID，保留原 PVC UID/引用保护，不新增 PVC schema。
9. VM requested namespace非空时必须等于标准名；Instance trusted namespace只作精确核对，wiring复用同一规则。
10. ModelDev caller验证认证 subject、tenant相等、cluster allowlist与真实writer连接、标准namespace及原UID；不接受任意目标。
11. 镜像使用该tenant image space的单authority/robot；拆分 Namespace network归属与 Secret原ani.io writer标签；Res只写Secret，不写SA。
12. 删除ModelDev旧namespace/owner-label白名单语义且拒绝旧非空配置；Inference caller保留原静态分支，现场变更交09。

## Acceptance

- NS01：所有本次 owner通过01统一向量；大小写或空白不得自动修正，Gov数字ID不能替代resource UUID。
- NS02：缺失、错属、非Active、删除中及UID变化拒绝；Fake Kubernetes actions明确没有 Namespace create/patch。
- RS01：真事务覆盖并发接纳不同UID冲突；回填只更改精确NULL行，已有UID及无关网络记录保持。
- RS01：Storage replay、Sandbox404、VM/Instance目标覆盖关键入口；Namespace404与provider404保持不同判断。
- RS01、IM01：ModelDev标准动态Secret写允许，跨tenant/cluster/UID/registry拒绝；Inference原caller行为回归通过。
- 现场回填前后零NULL证据归09；本项提交工具、migration及组件证据，不提前宣称在线数据库已切换。

## Constraints

- 不新增 Res Namespace RPC、通用Secret API、Namespace Create/Patch/Adopt/Delete或Harbor初始化。
- 不修改历史migration，不加第二个NOT NULL migration，不把NULL当兼容路径，不删除共享记录凑零NULL。
- 不改Inference、VM/Sandbox/Instance状态机、套餐/配额或PVC schema，不批量迁移无关资源。
- 不授ModelDev任意namespace、registry或secret写；不将Secret writer标签替换为Namespace标签。
- 在09维护窗口前不得应用新Res配置/回填或停写；重型验证按仓库规则在批准远端执行。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。Resource原migration/原UID回填/零NULL及67d正式切换完成，无新Namespace RPC；见 evidence/09/res-recovery-v3-final-window-actual.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: PASS（02组件实现与必要验证）；现场回填/切换/A-B业务验收 NOT_RUN，归09/10，未提前记LIVE通过。
- Base / final commits: `23e25b33ca02890231cd273ef7246b0fc415476c` → `71b3ad7a2a88ae531e5e7b80abde2f494ff28d9d`；分支 `codex/modeldev-dynamic-tenant-environment` 已通过SSH github:443推送。
- Changed files / generated artifacts: Res纯 `internal/tenantnamespace`、Network事务Namespace UID及0025 migration、默认只读+reviewed SHA条件回填工具、KC/VM NAD原UID护栏、Sandbox只读无创建/无可选绕过、Storage replay与写前UID、VM/Instance标准目标、动态ModelDev Secret caller、旧prefix配置拒绝、RBAC示例、conf.pb.go/sqlcgen生成物；Inference静态caller保持。
- Validation commands / exit codes / required gates: Fedora `GOTOOLCHAIN=auto GOMAXPROCS=2 GOFLAGS=-p=2 make config sql` exit0；`./scripts/integration -timeout 5m -run 'TestTenantNamespace(ConcurrentAdmission|Backfill)' ./internal/data/network` exit0（真实任务独立PG且已清容器）；仓库强制 `make verify` exit0，覆盖生成确定性/边界/故障构建/tidy/default tests/vet/build/module verify/diff。只选必要PG测试，未跑全量integration/live。无本地生成/测试/构建，无Fedora打包传输。
- Acceptance IDs / actual values / evidence paths: NS01/NS02/RS01/IM01 组件PASS；统一向量SHA256 `1e7eb5561043b5fe172e8bcf18a173cd3b2deb90804ded802a55ed06d1bc882a`。Res `docs/execution/records/MODELDEV-TENANT-ENV-20261101/README.md` 及 generation.log/namespace-pg.log/verify.log，880输入文件manifest SHA256 `f26ffcafb6a4d5c11acb152d8e2b0044d26441e9d4fb5ff3a9c3135b9a3a3239` 与Fedora验证源匹配exit0。
- Deployment or cleanup effects / preserved assets: 没有连接集群写资源、没有在线migration/backfill/部署/旧数据删除；仅独立测试PG运行/清理。现存Network/VM/Instance/PVC及Inference配置未现场变动。
- Remaining blockers / affected dependencies: 09需要停新Network身份接纳→0025→工具plan/apply→验零NULL→上线新Res并撤旧prefix和ModelDev旧namespaces/owner-label配置；不允许旧Res恢复写NULL。工具入口 `go run ./scripts/tenant-namespace-backfill -dsn-file <runtimeDSNfile> -cluster-id <cluster> -kubeconfig <approvedPath>`，apply另加 `-apply -reviewed-sha256 <planSHA>`。真实cluster/namespace/UID证据与切换由09补。
- Contract conflicts / evidence / minimal correction: 首次强制verify证实收紧Instance新目标解析会误拒原历史metric scope（`TestMetricScopePersistedReadOnlyHistory`/VM fallback）。按spec2.2/12.3原记录保留约束，历史只读scope独立做DNS语法校验并保持tenant-scoped原数据库位置；新Create/Network目标仍严格标准名。该修正不增加ModelDev兼容reader。最终门禁PASS。

## Comments

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。


- 2026-11-01：02组件完成并推送；验收边界及09维护输入见Result。

补充切换维护实现67dceef03f01215c294a55f11464e3e3a52cb783：真实PG4项race及make verify均exit0，本地/Fedora8458文件一致；未改API/migration/dependency或普通runtime接线。见evidence/09/res-uid-window-final.json与res-namespace-window-preparation.md；现场所有写步骤NOT_RUN。
