# CPU 受管微调 · 模型登记链闭环验证报告（102.68）

验证日期：2026-10-21
验证环境：`192.168.102.68`（`kubekey` 集群，kubeconfig context `kubernetes-admin@kubekey`）
验证对象：ModelDev → Model 的**训练产物登记链**（Prepare → 受控复制 → Complete → READY → 重放）
对应文档：[执行主链](finetuning-core-flow.md)、[手动验收手册](finetuning-manual-test.md)、[P0 执行计划](finetuning-p0-execution-plan.md)、[设计基线](design/ani-finetuning-v4.2.2/spec.md)

---

## 0. 范围与边界（先读）

- 本轮**只覆盖登记链一段**：ModelDev 侧以冻结的产物事实调用 Model 的三个登记 RPC，完成受控复制与 READY 落账。
- **不覆盖**：训练执行（KFP/Trainer）、export/verify、collect/publish、Gov/BFF 真实来件、D09.3 派发仲裁、停止/deadline、Inference 消费新版本。
- **身份口径**：102.68 的 Model 构建只授权 `ani-governance` 与 `ani-inference.ani.internal`，环境内不存在 `ani-modeldev` 客户端证书。本轮按明确决策**借用 `governance-client-tls`** 作为隔离测试驱动身份，证据中已写入 `client_identity_scope` 标注。它**不是**生产 ModelDev 运行身份，**不构成 MR01 正式验收**。
- **运行形态**：102.68 上没有 ModelDev 服务部署。本轮使用带 `finetuning_model_registration` 构建标签的测试二进制，在集群内临时 Pod 中直接驱动，跑完即删除 Pod；未改动任何共享工作负载。
- 本文档不记录任何凭据、私钥、预签名 URL 或对象字节。

---

## 1. 结论摘要

| 环节 | 结果 | 关键事实 |
| --- | --- | --- |
| Prepare | **通过** | `registration_id = d0cb74cf-0734-4b05-803b-4657ee52e170`，ticket 有效期 `09:54:00Z` |
| 受控复制 | **通过** | 真实 S3 GET + 单次 PUT，`999602688` 字节，重算 SHA256 与冻结值一致 |
| Complete | **通过** | 新建 model `ce0279cb…`、version `2fa08219…`，状态 `ready` |
| 重放 | **通过** | 同 session + 同幂等键返回同一持久回执，`replay_identical = true` |
| 落库读回 | **通过** | registration `ready`；artifact `provider = upload`，路径/大小/SHA 均一致 |
| 失败会话处理 | **通过** | 第一次复制失败的会话如实记为 `aborted`（`driver copy failed`），未静默吞掉 |

结论：**登记链在 102.68 上端到端跑通**，状态流转 `pending → uploaded → ready` 与设计一致。

---

## 2. 环境与基线

| 项 | 实测值 |
| --- | --- |
| Model 镜像 | `docker.changqingyun.cn/ani/model-service:release-20261021-1141` |
| Model Pod | `ani-model-service-d865b797d-k5td6`（`1/1 Running`，节点 `ani-k8s-worker-1`） |
| gRPC / 管理端口 | `19090` / `19091`，Service `ani-model-grpc`（`10.233.7.99`） |
| 服务端证书 | Secret `ani-model-server-tls`，SAN `DNS:ani-model.ani.internal`，CA `ANI Internal CA` |
| 客户端证书 | Secret `governance-client-tls`，SAN `DNS:ani-governance`，有效期至 2036-10-17（借用身份，见 §0） |
| 数据库 | `ani-infra/postgres-0`，库内已有 `trained_artifact_registrations` 表 |
| 对象存储 | MinIO `ani-infra/minio-0`，`http://minio.ani-infra.svc.cluster.local:9000`，bucket `ani-models`，`tenantBuckets=false` |
| 驱动源码基线 | `ani-modeldev-service` commit `78ffce6` + 本轮修复 `0f39424` |
| 驱动二进制 | linux/amd64，标签 `finetuning_model_registration`，`main.Version` 注入为 `78ffce64dac9a637a6dd19b05ede6b989cb5afcf` |

### 2.1 二进制与能力实证（先于联调完成）

| 检查 | 结果 |
| --- | --- |
| 三个 RPC 是否在该构架中存在 | 镜像二进制含 `PrepareTrainedArtifact` 51 处、`CompleteTrainedArtifact` 38 处、`AbortTrainedArtifact` 31 处 |
| 授权身份名单 | 仅 `ani-governance`、`ani-inference.ani.internal`；`ani-modeldev` 出现 0 次 |
| 方法级拒绝行为 | 以 inference 身份调用 Prepare → `PermissionDenied`（而非 `Unimplemented`），证明方法已注册、由策略拦截 |

---

## 3. 执行链路与实测结果

### 3.1 Prepare

| 项 | 值 |
| --- | --- |
| 租户 | `a6150f60-2586-4827-9e32-04222065f50e` |
| 源版本 | `14e772ee-e86b-4300-8354-c93ebdb9a7c8`（`ready`，`safetensors`，`999602688` 字节） |
| 分支 | 新建容器（`target_model_id` 为空，`model_name = ft-registration-10268-20261021`） |
| 返回 registration | `d0cb74cf-0734-4b05-803b-4657ee52e170` |
| 上传目标 | `a6150f60-…/trained-artifacts/d0cb74cf-…/model.tar` |
| 回显大小 / SHA | `999602688` / `e1fc761d399f82372857717c6b38c87ad238f9dc85cf033111b7b2d07bb89e1c`（与冻结值一致） |
| ticket 有效期 | `2026-10-21T09:54:00.781568197Z` |

### 3.2 受控复制

- 源对象真实键：`a6150f60-2586-4827-9e32-04222065f50e/imports/64991e37-c1e5-441c-b17a-2ccc80d7261f/model.tar`
- 读取源对象一次，写出一次 PUT；**不重试、不重选 bucket/key/version**
- 落盘字节 `999602688`，流式计算 SHA256 `e1fc761d…` 与冻结值一致
- 源对象 `VersionId` 为 `null`（bucket 未开启版本化），已按观测事实记录

### 3.3 Complete

| 项 | 值 |
| --- | --- |
| registration | `d0cb74cf-0734-4b05-803b-4657ee52e170` |
| model | `ce0279cb-91fa-458e-88f5-251b0ac18e39`（新建，`status = ready`） |
| model_version | `2fa08219-4dd7-4513-b633-42cd6c964ef9`（`status = ready`） |
| 运行态继承 | `engine_type = vllm`，来自源版本，未显式传 engine 三元组 |
| artifact | `9bc80177-fbf5-42cc-8213-7a0543ddee98`，`provider = upload`，reference 指向新对象 |

### 3.4 重放

同一 `registration_id` + 同一幂等键再次 Complete，返回与原回执逐字段相同的结果（`replay_identical = true`），未产生第二个版本。

### 3.5 落库读回

| 表 | 读回结果 |
| --- | --- |
| `trained_artifact_registrations` | `status = ready`，`created_at 09:39:00.777Z → updated_at 09:39:18.286Z`，`error_message` 为空 |
| `models` | 新模型存在且 `status = ready` |
| `model_versions` | 新版本 `ready`，`size_bytes` / `checksum_sha256` 与冻结值一致 |
| `model_artifacts` | `provider = upload`，reference/size/sha 与 version 行一致 |
| 失败会话 | `db28f911-ed50-43f7-a4f5-5bd6ab9e8855` → `aborted`，`driver copy failed` |

---

## 4. 本轮修正与发现（影响后续复现）

1. **驱动缺陷（已修，commit `0f39424`）**：`registrationPrepare()` 只从 fixture 元数据构造请求，未把冻结对象的 `size_bytes` / `checksum_sha256` 填入 `TrainedArtifactPrepare`；而 ticket 是用这两个值回填的，随后断言 `ticket == object` 必然失败。已在 [finetuning_model_registration_test.go](../cmd/ani-modeldev-service/finetuning_model_registration_test.go) 中改为取自冻结对象，与生产调用路径一致。

2. **源对象键必须带租户前缀**：`model_artifacts.reference` 存的是相对路径 `imports/<artifact-id>/model.tar`，MinIO 中实际键为 `<tenant>/imports/<artifact-id>/model.tar`。用无前缀键会得到 `UPLOAD_UNAVAILABLE`。

3. **源版本所属模型被删除时不可用**：最初选用的 `qwen2.5-0.5b-instruct-real` 其 model 行 `status = deleted`，Model 解析源版本时过滤掉，返回 `MODEL_NOT_FOUND: no rows in result set`。改用活跃模型的 `ready` 版本后通过。

4. **new-model 分支的必填项**：缺 `model_name` 返回 `model_name is required for a new model`；缺 `capabilities` 返回 `capabilities are required for a new model`。

5. **生产对象传输在本环境有两个前置不成立**，已在证据中作为观测事实记录，而非静默替代：
   - 桶未开启版本化，源对象 `VersionId` 恒为 `null`，而生产传输要求具体非空版本；
   - Model 以 `http` 预签名（存储客户端未启用 TLS），而生产传输只接受 `https`。
   因此复制使用驱动内的声明式测试复制器（真实读 + 真实单次写），不使用生产 `ArtifactUploader`。

---

## 5. 整条微调闭环：已测与未测

| 阶段 | 是否测 | 原因 |
| --- | --- | --- |
| 受理与意图冻结（Gov → ModelDev） | 未测 | 102.68 无 ModelDev 部署；ani-01 的 ttyd 通道中断，无法操作现网 ModelDev |
| 训练执行（KFP/Trainer Run、JobSet） | 未测 | 本轮范围为登记链；且 ani-01 不可达 |
| export / verify（独立进程、greedy 短推理） | 未测 | 依赖上一阶段产物，未执行训练 |
| collect / publish（18 文件发布与 bundle） | 未测 | 同上 |
| **Model 登记（Prepare → 复制 → Complete → READY）** | **已测** | 本报告主体 |
| D09.3 派发仲裁（每写取 permit、事务外 RPC） | 未测 | 驱动不覆盖；已有真实 PostgreSQL 生命周期用例单独覆盖，证据中标注 `NOT_RUN` |
| 停止 / deadline / 重启接管 | 未测 | 需要真实 Run 生命周期 |
| 正常关闭（NATURAL_TERMINAL 收据） | 未测 | 同上 |
| Inference 独立加载新版本（FT29） | 未测 | 需本次登记的版本进入推理链路 |
| Gov/BFF + 前端真实来件 | 未测 | 未接通真实入口 |
| ModelDev 服务自身部署与运行（`command.model` 配置、mTLS 身份） | 未测 | 102.68 无部署；ani-01 上仍是旧镜像 `20261014-cursorkey` |

**边界重申**：登记链通过**不等于**整条微调闭环通过。它证明的是「冻结事实 → 受控复制 → 供应方结算 → 持久 READY → 原键重放」这一段在真实 Model mTLS 入口与真实 S3 上成立。

---

## 6. ani-01 恢复后的待测清单

按依赖顺序，前两项是硬前置：

1. **恢复通道**：ttyd `139.198.29.154:7681` 当前 TCP 可连但零字节关闭（后端进程不再应答）；SSH `22` 可用但需凭据。任一恢复即可继续。
2. **确认 ani-01 的 Model 构建能力**：镜像 `localhost/ani-model:0b687e3-20261021-r2` 与 annotation `source-revision: 430d35aa…` 不一致，需实测运行二进制是否含三个 RPC、授权哪些身份。
3. **身份阻塞项**：若 ani-01 Model 同样只授权 `ani-governance`，正式路径需 Model 在 `policy map` 与 `expectedIdentities` 两处登记 `ani-modeldev`，并签发对应 SAN 证书；在此之前只能做隔离测试。
4. **部署新 ModelDev**：镜像 `172.16.101.10:5000/ani/ani-modeldev-service:20261021-registration` 已在共享 registry；需给 `md-fixed-config` 的 `config.yaml` 增 `command.model` 段并挂载证书，再 rollout（回滚目标 `20261014-cursorkey`）。
5. **闭环资源**：`ani-ft-core-20261014` 的 PVC 配额 `19/19`、storage `380Gi/380Gi` 已满，跑新用例前需环境侧扩容。
6. **串联顺序**：真实训练 → export/verify → collect/publish → 本报告已验证的登记链 → Inference 加载 → Gov/BFF 真实来件。

---

## 7. 证据与复现

- **证据 JSON**（本地留存）：`local-remote/deploy-work/registration-evidence-10268.json`
- **证据 JSON**（环境留存）：`/tmp/evidence-10268/` 下三份，分别对应失败、复制失败、成功三次运行
- **驱动构建**（在具备 go1.26.9 的机器上，交叉编译到 linux/amd64）：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
go test -c -tags finetuning_model_registration \
  -ldflags "-s -w -X github.com/zhangzhe-ctrl/ani-modeldev-service/cmd/ani-modeldev-service.Version=78ffce64dac9a637a6dd19b05ede6b989cb5afcf" \
  -o mdreg.test ./cmd/ani-modeldev-service/
```

- **驱动运行**（fixture 路径由环境变量给出，`evidence_directory` 必须为 `/evidence`）：

```bash
ANI_FINETUNING_MODEL_REGISTRATION_FIXTURE=/tmp/fixture.json \
  /tmp/mdreg.test -test.run TestFinetuningModelRegistration -test.v
```

- **fixture 字段来源**：租户/源版本/大小/SHA 取自 Model 数据库 `models`、`model_versions`、`model_artifacts` 三表；存储端点与凭据取自 Model 部署配置与 Secret；身份材料取自 `governance-client-tls`。

---

## 8. 遗留与未做

- 未删除本轮产生的登记记录与新建模型（`ft-registration-10268-20261021`）：Model 未提供清理入口，且删数据不属于本次范围。如后续需要清理，应走 Model 的删除接口而非直接改库。
- 未改动 RustFS/MinIO、VAP/CNI 或共享 Model/Gov/Inference 部署。
- 未修改 102.68 上的任何持久配置；临时运行 Pod、探针脚本、主机上的证书与凭据副本均已删除。
