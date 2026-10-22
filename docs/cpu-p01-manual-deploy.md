# CPU-P01 手动部署与当前状态

更新日期：2026-10-10。面向环境交付、运维与验收人员。

对应指南：[CPU-P01 功能、实现、扩展、部署与使用指南](cpu-p01-guide.md) 与 [手动复部署与测试手册](cpu-p01-manual-test.md)。本文记录本次手动部署（`manual-ani-system-20261008`）的实际落地结果与当前边界，供验收接续使用。

## 1. 环境与目标

| 项 | 值 |
| --- | --- |
| 集群 | 共享 GPU/CPU 集群，节点涉及 `ani-01`、`ani-03` |
| 目标命名空间 | `ani-system`（原 `ani-cpu-p01-system` 重命名映射） |
| 租户命名空间 | `ani-kfp-manual-a`、`ani-kfp-manual-b` |
| 部署包目录 | `/home/chabking/workspace/cpu-p01-20260930-01/manual-ani-system-20261008/` |
| 私有材料目录 | `.private/`（`0o600`，不作 Git 提交） |
| 管理标识 | `ani.io/managed-by: ani-manual-20261008` |
| ModelDev 运行镜像 | `localhost/ani-cpu03@sha256:53f1...b5040` |

## 2. 运行中的服务

当前三个部署在 `ani-system` 全部 `READY 1/1`：

| Deployment | Namespace | READY | 说明 |
| --- | --- | --- | --- |
| `ani-modeldev` | ani-system | 1/1 | 训练执行事实服务，`config` 卷引用 `md-fixed-config` |
| `ani-governance` | ani-system | 1/1 | Governance BFF，已配置 ModelDev 集成环境变量 |
| `ani-modeldev-exit-retention` | ani-system | 1/1 | mTLS 退出保留后端，webhook 指向其 443 |

### 2.1 ani-modeldev 关键配置

`ani-modeldev` Deployment 的 `config` 卷挂载的 ConfigMap 为 **`md-fixed-config`**（`immutable=false`）：

- 内含 4 个手动 facts（success / fail / stop / deadline）及各 Release 的证明摘要
- `environment_evidence.reference` 与 `application_evidence.reference` 均指向 `environment-receipt.json`，**不含 `#rbac_checks` 片段**，满足 `admissionfacts.validEvidence` 字符白名单（仅 `._:/-` 与字母数字）

> 注意：集群中仍存在同名旧 ConfigMap `modeldev-runtime-config-grpc30-37d6a354-358534c3`（`immutable=true`，facts 含 `#` 片段）。它**未被任何部署引用**，是历史占位；**不要将部署引回该 ConfigMap**，避免 Admission facts 校验再次失败。

### 2.2 修复记录

初始 `ani-modeldev` CrashLoopBackOff 根因：`application_evidence.reference` 携带 `#rbac_checks`，被 `admissionfacts.validEvidence` 白名单拒绝 → `ErrInvalidFacts` → 监听器配置失败。

处置：将生产 ConfigMap 中的 4 个 facts 去除 `#rbac_checks` 片段、重算 sha256，写入新 ConfigMap `md-fixed-config` 并让 Deployment 卷引用切换。`rollout` 后 Pod `1/1 Running`，9000/9001/9002 全部监听，healthz/readyz 正常。

## 3. Catalogue 与 Release

| 数据 | 内容 | Job 结果 |
| --- | --- | --- |
| Catalogue PVC | `modeldev-catalogue-block`（Bound） | `modeldev-catalogue-restore` Completed PASS（32 files） |
| 手动 Release | success / fail / stop / deadline 4 条 | `modeldev-manual-releases` Completed PASS |
| 工作区探针 | 租户 A / B 各 1 次原子读写验证（UID/GID 10001） | `manual-workspace-probe` 租户 A/B 均 Completed PASS |

render 重新产出后，`application.json` 与 `release-imports.json` 中 `rbac_checks` 计数为 0，4 个 facts 的 reference 干净。

## 4. Webhook

`ani-modeldev-exit-retention` MutatingWebhookConfiguration 已创建：

| 项 | 值 |
| --- | --- |
| Webhook 名 | `ani-modeldev-exit-retention` |
| `failurePolicy` | `Fail` |
| `clientConfig.service` | `ani-system/ani-modeldev-exit-retention:443` |
| `timeoutSeconds` | **10**（原为 2，见下） |
| 后端 Endpoint | Running，Pod `1/1` |

> ⚠️ `timeoutSeconds` 已从 2 调到 **10**：2s 对"每次冷建 TLS、握手串行化"的 Python 后端太紧，会导致 KFP 关闭阶段 Pod 被拒、执行卡在 `close_state=OPEN`。修复命令见 [验证记录 §5.3](cpu-p01-manual-verify.md)。**`manual.py render` 不覆盖该字段，重新 render/deploy 会把 2s 打回**，需固化此 patch。

## 5. 当前边界 / 待办

部署与 watchdog 链路已落地；**核心正向业务验证已于 2026-10-09 跑通**（`enable → import-csv → run → wait → verify`，`L4_PASS`），并于 2026-10-10 **复演一次**（`bprime-03`）；**反例矩阵 `t negatives` 亦已通过**（7 项断言）。详见 [验证记录](cpu-p01-manual-verify.md)。

仍未执行：

1. **非成功路径复演**（手册 §8，fail / stop / deadline）：可直接用对应预设继续。
2. **正式清理** `t cleanup-plan/apply`：需先选定目标执行。
3. **webhook `timeoutSeconds` 固化**：见 §4，`render` 会打回 2s。
4. **`modeldev-mtls` 客户端证书换发**：2026-10-12 到期。

## 6. 相关文件

- 部署入口与命令：`scripts/cpu-p01-manual/manual.py`（`prepare|pipeline|render|forward|kubectl`）
- 业务测试：`scripts/cpu-p01-manual/test.py`（`login|catalogue|enable|run|wait|verify|negatives`）
- 验收实现：`scripts/cpu-p01-manual/acceptance.py`