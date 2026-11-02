# 01 基线、部署输入与公共合同冻结

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

固定本次实施的代码基线、可知部署输入和跨仓公共合同，使后续任务按同一协议实施。
依据：[spec.md](../spec.md) §0–4、§6–7、§15–17；本项不以尚未部署的新运行证据作为完成前置条件。

## Scope

- 三仓：ani-modeldev-service、ani-governance、ani-resource-service；仅基线记录、必要公共合同与正式 API 源文件。
- ModelDev：api/ani/modeldev/v1/tenant_environment.proto、modeldev.proto、v4 相关 source proto 与 contract/cpup01/ 的字段合同。
- 执行包：deployment-inputs.json、contracts/、验收结果模板和实施基线记录；保留原 verification 证据的实际结论。
- Gov：正式 ModelDev API module 的发布/消费约定；本项不实现 BFF 业务，后续由07接入。
- 工作目录：.scratch/modeldev-dynamic-tenant-environment/；后续文件归属、分支和公共生成物合并顺序在此登记。

## Implementation

1. 重新读取三仓远端 main，记录精确 SHA；与 spec 基线有差异时只核对相关 diff，记录影响，不默认为旧 SHA 仍最新。
2. 建立隔离工作树和各任务文件归属；共同 proto、快照字段与全局 wiring 先固定，避免05/06/07各自修改出不同协议。
3. 从现有证据填入已知的集群五项身份、A/B resource UUID、标准 Namespace/UID、KFP 版本及旧环境定位线索。
4. deployment-inputs.json 只保存非敏感值或文件引用；每个待补项记录负责任务、所需证据和启用门槛。
5. 真实 token 正向调用、实际轮换、最终四定义 ID、最终发布 imageID 等留给08/09/10，不要求本项取得新系统的运行结果。
6. 冻结统一 Namespace 名称、两项 network 标签和 canonical 非零 UUID 规则；建立同一份正反测试向量。
7. 冻结环境服务的 Ensure/Get、13字段 view、optional 字段、State/Phase、有限 reason 与 ErrorReason 38，保留既有 reserved 编号。
8. 冻结四参数 IR、v4 Snapshot.DispatchOwner、三个 SA UID、ManagedImageAccess、环境投影 canonical 及摘要边界。
9. 冻结唯一表、自然键、专用连接 advisory lock 算法、先登记后锁、25/28/30秒预算和显式重试语义。
10. 冻结固定对象、RBAC/native policy 名称、单受管 registry/platform_project、KFP SecretRef 与两种 token audience 合同。
11. 按仓库工具生成公共 API 产物并安排正式 module 发布；后续07只消费正式 pin，不复制生成物或使用本地 replace。
12. 在验收模板登记所有 NS/EN/API/SNP/ST/DP/CL/IM/RS/AD 与 LIVE 编号，初始 NOT_RUN；记录后续每项证据责任。

## Acceptance

- NS01：公开向量覆盖标准 A/B UUID，以及大写、数字ID、零UUID、空白、无连字符、截断反例；实现通过由02/04补齐。
- EN04、SNP01：投影与 v4 字段、规范字节边界、严格 reader 和未知字段规则可供03/04/06直接实现。
- API01、API02：方法名、tag、ErrorReason 38、错误白名单和预算合同无歧义；行为验证由03/07完成。
- LIVE01：已知输入有来源；所有未知项明确标为 NOT_RUN/BLOCKED及负责后续任务，不伪称现场已证实。
- LIVE03–LIVE06：最终环境、材料 ID、token轮换和 A/B运行证据明确归09/10，不成为01解除依赖的门槛。
- 三仓 base SHA、公共合同生成命令/结果、字段差异和文件责任已记录；后续不需要重新自由选型。

## Constraints

- 不得修改 spec 已固定的架构、字段、权限、状态或任务依赖；事实冲突按 Result 留证并交回裁定。
- 不得在本项连接集群执行资源写入、清理旧环境或将新运行材料覆盖到正在服务的旧部署。
- 不得输出 token、Secret data、DSN、STS 会话或完整环境变量；待填不能用猜测值充当事实。
- 不得把所有 LIVE 验收完成设为本项依赖，否则与后续部署形成环；只冻结合同和可知输入。
- 重型构建/必需 gate 在批准的远端 Fedora 执行；git push 使用 SSH，不扩展其他仓库或账户。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。完整指定集群身份、固定源码/镜像、部署输入及合同已回填；最终共享进程与namespace对账见 evidence/10/completion-multitenant-regression/final-coexistence-and-process.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: PASS (source baseline and common contract freeze only).
- Base / final commits: ModelDev 21bf4bd6540d462a61c91b629f26250899583223; Gov ddd98f4c79751ba848fa862bab6878dc7bc99bb7; Res 23e25b33ca02890231cd273ef7246b0fc415476c. ModelDev final source d807cbdbd5dd6664d61480acedc752e19cfca48a and Resource final source 71b3ad7a2a88ae531e5e7b80abde2f494ff28d9d; both required make verify exit0, SSH pushed. Gov final pin/gate recorded in07.
- Changed files / generated artifacts: tenant_environment.proto/pb.go/grpc.pb.go, ErrorReason 38, v4 snapshot fields and current protobuf output; file ownership and tags in evidence/contract-freeze.md.
- Validation commands / exit codes / required gates: Fedora make api exit 0; make config exit 0; original package SHA256SUMS check exit 0. Final integrated make verify PASS, exit0; evidence/modeldev-final-make-verify.log. This is source/isolated-PG evidence, not live acceptance.
- Acceptance IDs / actual values / evidence paths: NS01/EN04/SNP01/API01/API02 contracts frozen by supplied spec and proto, behavior delegated to02/03/04/07; LIVE01 current identity check evidence/baseline.json, remaining live inputs owned08/09/10. Not a live PASS.
- Deployment or cleanup effects / preserved assets: no cluster mutation; A/B namespaces original UIDs/Active/network labels verified, preserved.
- Remaining blockers / affected dependencies: final API module pin follows integrated verified commit. New token auth/rotation, four published definitions and final imageIDs remain NOT_RUN for08/09/10 and do not block common field implementation.
- Contract conflicts / evidence / minimal correction: current verify-source only accepts baseline migration; exact source conflict and two-migration minimal gate change recorded in evidence/contract-freeze.md. No identity or permission change.

## Comments

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。
