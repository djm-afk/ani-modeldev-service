# CPU 受管微调 · 登记链闭环验证报告（ani-01）

验证日期：2026-10-21
验证环境：`ani-01`（`172.16.101.10`，k8s v1.35.8，kubeconfig context `kubernetes-admin@kubekey`）
验证对象：ModelDev → Model 的**训练产物登记链**（Prepare → 受控复制 → Complete → READY）
对应文档：[执行主链](finetuning-core-flow.md)、[手动验收手册](finetuning-manual-test.md)、[P0 执行计划](finetuning-p0-execution-plan.md)、[102.68 同类验证](finetuning-registration-chain-10268-20261021.md)、[设计基线](design/ani-finetuning-v4.2.2/spec.md)

---

## 0. 范围与边界（先读）

- 本轮覆盖 **ModelDev → Model 的登记链一段**，并在 ani-01 的真实 Model mTLS 入口、真实 RustFS 对象存储上完成。
- **同时完成了 ModelDev 服务的真实部署**：新镜像 `20261021-registration` + `command.model` 配置 + 证书挂载，进程 1/1 Running、0 重启。
- **身份口径（按明确决策执行）**：ani-01 的 Model 构建只授权 `ani-governance` 与 `ani-inference.ani.internal`，二进制中 `ani-modeldev` 出现 0 次。本轮按决策**借用 `ani-governance` 客户端证书**作为调用身份，`actor` 使用 `governance:` 前缀。这是隔离测试口径，**不构成 MR01 正式验收**。
- **不覆盖**：训练执行（KFP/Trainer）、export/verify、collect/publish、Gov/BFF 真实来件、D09.3 派发仲裁、停止/deadline、Inference 消费新版本。
- 本文档不记录任何凭据、私钥、预签名 URL 或对象字节。

---

## 1. 结论摘要

| 环节 | 结果 | 关键事实 |
| --- | --- | --- |
| ModelDev 部署 | **通过** | 镜像 `20261021-registration`（`78ffce6`），1/1 Running，**0 重启**（对比旧镜像 15 次重启） |
| `command.model` 装配 | **通过** | `withModelRegistration` 未 fail-closed；模型配置、CA、客户端证书、artifact_plan 全部就位 |
| mTLS 互信 | **通过** | ModelDev 材料与 Model 服务端握手 `Verify return code: 0` |
| Prepare | **通过** | `registration = 0d7830a4-a6fe-438e-90b3-7e1a70293744` |
| 受控复制 | **通过** | 真实 S3 GET + 单次 PUT，`950272` 字节，`status=200`，重算 SHA256 与冻结值一致 |
| Complete | **通过** | 新建 model `3026fac6-6a01-46a1-8116-18289538c236`、version `392a50d0-0061-4147-a2a4-b284d2b7d980`，`ready` |
| 落库读回 | **通过** | registration `ready`；artifact `provider = upload`，路径/大小/SHA 一致 |

结论：**登记链在 ani-01 上端到端跑通**，且运行在真实 ModelDev 进程的配置之上，而非仅测试驱动。

---

## 2. 环境与改动清单

### 2.1 环境基线（实测）

| 项 | 值 |
| --- | --- |
| 集群 | ani-01/02/03 均 Ready，v1.35.8；containerd 2.3.4 |
| Model 镜像 | `localhost/ani-model:0b687e3-20261021-r2` |
| Model 二进制能力 | `PrepareTrainedArtifact` 51 / `CompleteTrainedArtifact` 38 / `AbortTrainedArtifact` 31 处符号；授权身份仅 `ani-governance`、`ani-inference.ani.internal`，`ani-modeldev` 为 0 |
| 对象存储 | RustFS `ani-rustfs-svc.ani-platform.svc.cluster.local:9000`（TLS，CA 为 `ani-root-ca`），bucket `ani-models` |
| 数据库 | `ani-platform/postgresql-0`，库 `ani_model`，`trained_artifact_registrations` 就绪 |
| 客户端证书 | `governance-modeldev-client-tls`，SAN `DNS:ani-governance`，有效期至 2027-01-10 |

### 2.2 本轮对共享环境的改动

| 对象 | 改动 | 可回滚方式 |
| --- | --- | --- |
| `ani-system/ani-modeldev` Deployment | 镜像 `20261014-cursorkey` → `20261021-registration`；新增 volume `model-client-tls`；新增 mount `/etc/modeldev/model-client`、`/etc/modeldev/ca/model-ca.crt` | 回滚镜像到 `20261014-cursorkey`，删除两个挂载 |
| `ani-system/md-fixed-config` ConfigMap | `config.yaml` 增 `command.model` 段；新增 key `model-ca.crt` | 恢复旧 `config.yaml`，删除 `model-ca.crt` |
| `ani-system/modeldev-model-client` Secret | 新建，复制自 `governance-modeldev-client-tls`，仅含 `tls.crt`/`tls.key` | 删除该 Secret |
| `ani-ft-core-20261014/core-flow-budget` ResourceQuota | `persistentvolumeclaims` 19 → 25；`requests.storage` 380Gi → 500Gi | 改回原值 |
| Model 侧数据 | 新增 1 条 registration、1 个 model、1 个 version、1 条 artifact | 走 Model 删除接口 |

`command.model` 的关键取值：

```yaml
model:
  endpoint: ani-model-grpc.ani-system.svc.cluster.local:19090
  ca_file: /etc/modeldev/ca/model-ca.crt
  client_dns_name: ani-governance
  certificate_file: /etc/modeldev/model-client/tls.crt
  private_key_file: /etc/modeldev/model-client/tls.key
  server_name: ani-model.ani.internal
  actor: governance:modeldev-finetuning-registration
  timeout: 30s
  artifact_plan:
    file_name: model.tar
    format: safetensors
    display_name: Finetuning trained artifact
    description: Trained artifact published by the managed finetuning pipeline
    capabilities: [text-generation]
```

---

## 3. 执行链路与实测结果

### 3.1 Prepare

| 项 | 值 |
| --- | --- |
| 租户 | `46936e6b-3a3b-4868-8d88-6833665abb70` |
| 源版本 | `b34a8f4c-cd4d-41fc-877e-49487cb91e48` |
| 分支 | 新建容器（`model_name = ft-reg-ani01-final`） |
| registration | `0d7830a4-a6fe-438e-90b3-7e1a70293744` |
| 上传目标 | `46936e6b-…/trained-artifacts/0d7830a4-…/model.tar` |
| ticket 传输 | `scheme=https`，host `ani-rustfs-svc.ani-platform.svc.cluster.local` |

### 3.2 受控复制

- 源对象：`ani-models/46936e6b-3a3b-4868-8d88-6833665abb70/imports/finetuning-registration-20261021/model.tar`
- 一次有界读取 + 一次 PUT，不重试、不重选
- 结果：`status=200`，`streamed=950272`，`sha256=a1abfab15d652e5bec830ceae73cb614294d280e5396edccfe40578300305df6`，与冻结值 `match=true`

### 3.3 Complete

| 项 | 值 |
| --- | --- |
| model | `3026fac6-6a01-46a1-8116-18289538c236`（`ready`） |
| model_version | `392a50d0-0061-4147-a2a4-b284d2b7d980`（`ready`） |
| 运行态继承 | `engine_type = vllm`（来自源版本，未显式传 engine 三元组） |
| artifact | `provider = upload`，reference 指向新对象 |

### 3.4 落库读回

| 表 | 结果 |
| --- | --- |
| `trained_artifact_registrations` | `status = ready`，`error_message` 为空，size/SHA 与冻结值一致 |
| `models` | `ft-reg-ani01-final`，`ready` |
| `model_versions` | `ready`，`size_bytes = 950272` |
| `model_artifacts` | `upload`，reference/size/sha 一致 |

---

## 4. 本轮发现的问题（影响后续复现）

1. **现成源对象与自身 DB 记录不一致**：ani-01 上唯一活跃可用源（`b34a8f4c…`）的 DB 记录 `size_bytes = 1519220224`、`sha256 = 4804e0c1…`，但 RustFS 中实际对象为 `1519266592` 字节。按 DB 值截断读取后算得 `3f256c86…`，与声明值不符。这是历史导入遗留的数据不一致，**不是本轮改动造成**。因此本轮登记链使用自建的确定性对象（950272 字节，尺寸与 SHA 完全受控）以完成严格校验。
   - 影响：若要在一个真实历史产物上验证登记链，需先核对并修正该产物的 DB 记录与其对象字节。

2. **Model 启动自检超时不可配置**：`check MinIO Storage` 的超时在 [main.go](../ani-model-service/cmd/ani-model-service/main.go) 中硬编码为 5 秒，无环境变量入口。Model 曾因此 CrashLoopBackOff（14 次重启），重启后恢复。按决策本轮**未改动 Model**。

3. **ModelDev 旧镜像反复崩溃**：旧镜像 `20261014-cursorkey` 因 liveness/readiness 探针失败重启 15 次（含 `no route to host`）。换成 `20261021-registration` 后 0 重启，对比明确。

4. **Model new-model 分支的必填项**：缺 `display_name`/`description` 返回 `display_name and description are required for a new model`；缺 `capabilities` 返回 `capabilities are required for a new model`；缺 `model_name` 返回 `model_name is required for a new model`。`artifact_plan` 已覆盖这些字段。

5. **actor 必须带 `governance:` 前缀**：否则 Model 的 `governancePrincipal` 直接返回 `Unauthenticated`。

6. **RustFS 预签名走 https**：客户端 PUT 必须携带 RustFS CA，否则 `x509: certificate signed by unknown authority`。

---

## 5. 整条微调闭环：已测与未测

| 阶段 | 是否测 | 原因 |
| --- | --- | --- |
| ModelDev 服务部署与 `command.model` 装配 | **已测** | 本轮完成，1/1 Running |
| **Model 登记（Prepare → 复制 → Complete → READY）** | **已测** | 本报告主体，真 Model + 真 RustFS |
| 受理与意图冻结（Gov → ModelDev） | 未测 | 未接通 Gov/BFF 真实来件 |
| 训练执行（KFP/Trainer Run、JobSet） | 未测 | 本轮范围为登记链；且集群无加速器，仅可走 CPU |
| export / verify（独立进程、greedy 短推理） | 未测 | 依赖训练产物 |
| collect / publish（18 文件发布与 bundle） | 未测 | 同上 |
| D09.3 派发仲裁（每写取 permit、事务外 RPC） | 未测 | 驱动不覆盖；已有真实 PostgreSQL 生命周期用例单独覆盖 |
| 停止 / deadline / 重启接管 | 未测 | 需要真实 Run 生命周期 |
| 正常关闭（NATURAL_TERMINAL 收据） | 未测 | 同上 |
| Inference 独立加载新版本（FT29） | 未测 | 需本次登记的版本进入推理链路 |
| Gov/BFF + 前端真实来件 | 未测 | 未接通真实入口 |
| 正式身份（`ani-modeldev` SAN 登记） | 未测 | 当前借用 `ani-governance`，见 §0 |

**边界重申**：登记链通过**不等于**整条微调闭环通过。

---

## 6. 后续待办

1. **正式身份**：Model 在 `policy map` + `expectedIdentities` 两处登记 `ani-modeldev-service`（ModelDev 自身证书 SAN 已是该值），并授权三个 RPC；之后把 `client_dns_name` 从 `ani-governance` 改回 `ani-modeldev-service`。
2. **数据一致性**：核对 `b34a8f4c…` 等历史导入产物的 DB 记录与对象实际字节，修正后才可用于真实产物登记。
3. **Model 启动自检**：若要消除周期性重启，需要 Model owner 把 5 秒硬编码超时改为可配置或放宽。
4. **训练链**：准备不可变镜像、Release、PipelineVersion 与 webhook，再跑 M01/M02 主链。
5. **资源**：配额已扩至 25 PVC / 500Gi（Ceph 可用约 105GiB，仍受后端容量约束）。

---

## 7. 复现方式

```bash
# 1) 部署（镜像 + 配置 + 证书）
kubectl -n ani-system set image deployment/ani-modeldev \
  modeldev=172.16.101.10:5000/ani/ani-modeldev-service:20261021-registration
# config.yaml 增 command.model；挂载 /etc/modeldev/model-client 与 /etc/modeldev/ca/model-ca.crt

# 2) 校验配置已加载
kubectl -n ani-system get cm md-fixed-config -o jsonpath='{.data.config\.yaml}' | grep -A12 '^  model:'

# 3) 触发登记（真实 Run 的 register 步骤会走同一编排）
#    独立验证时使用带 finetuning_model_registration 标签的测试驱动，
#    fixture 结构见 testdata/model-registration-fixture.example.json
```
