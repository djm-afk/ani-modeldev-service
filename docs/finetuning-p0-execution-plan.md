# CPU 微调 P0：开工执行计划（现场复核版）

编制日期：2026-10-16
范围：`finetuning-next-handoff.md` §3/§6 的 P0（Model 接入的最小真实登记链）

**文档关系**：

| 来源 | 文档 | 性质 |
| --- | --- | --- |
| 前一轮实现者（权威前提） | [验收手册](finetuning-manual-test.md)、[后续工作交接](finetuning-next-handoff.md) | M3-A 已完成的实现与交接说明 |
| 我方（接手方）初稿 | [实际调用表](finetuning-model-call-table.md)、[owner 问题函](finetuning-model-owner-questions.md)、[M3-A 集群复核](finetuning-m3a-cluster-review-20261016.md)、[P0 实施计划](finetuning-p0-plan.md) | 内部初稿，所有权在我方 |
| **本文** | — | 对上述全部做**现场只读复核**后的开工计划；含对初稿的 3 处修正 + 1 处新发现的现场故障 |
| 唯一规范 | `docs/design/ani-finetuning-v4.2.2/spec.md` | 冲突时以 spec 为准 |

本文不替代 spec，不下任何 PASS/BLOCKED 判定。

---

## 1. 现场复核结果（2026-10-16，全程只读）

### 1.1 【新发现·阻塞】Model 服务当前 CrashLoopBackOff，登记链的第一步就已经断了

四份既有文档都写"Model 已部署、1/1 Running"。**实测不成立**：

```
ani-model-service-7cf4895948-t6hr2   0/1   CrashLoopBackOff   7 (3m20s ago)   40h
```

容器退出原因（当前与上一次日志同）：

```
msg: "service terminated"
error: check MinIO Storage: storage provider error:
       Get "https://ani-rustfs-svc.ani-platform.svc.cluster.local:9000/ani-models/?location=":
       context deadline exceeded
```

即 **Model 启动时的 RustFS/MinIO 存储自检超时 → 进程直接退出**，liveness/readiness 19091 连接被拒 → 反复重启。事件里还有 `NodeNotReady`（约 22 分钟前）与 `SandboxChanged`。

同一时间窗内平台侧也确实不稳（restart 计数）：`ani-milvus-standalone` 32 次 / `postgresql-0`(ani-platform) 0/1 / `ani-milvus-etcd-0` 5 次 / `nats-0` 4 次 / `valkey-0` 3 次 / `ani-rustfs-*` 1 次(20 分钟前) / `ani-governance` 5 次 / `ani-inference-service` 7 次。三个节点现在都是 `Ready`。

**影响**：即使 Q1/Q2 今天回填、身份明天签发，**只要 Model 起不来，一条真实调用都发不出去**。这条不在 Model 代码仓里，属环境/共享存储问题，需环境 owner + Model owner 一起看。

**动作**：① 立即报环境 owner（RustFS 从 ani-03 到 `ani-rustfs-svc:9000` 的连通性/证书/超时）；② 把"Model 必须 1/1 Ready 且 `/readyz` 200"列为 Step 3 的硬前置（与 PVC 容量并列）；③ 在下一次发给 Model owner 的函里补一句"请确认当前实例为何 CrashLoop"。

### 1.2 【已定】部署配置的真实来源 = ConfigMap `md-fixed-config`（回答 B10 / T11）

```
Deployment ani-modeldev  args = ["-conf","/etc/modeldev/config.yaml"]
volumes: config -> configMap/md-fixed-config
CM ani-system/md-fixed-config keys:
  config.yaml  dispatch-binding.json  grpc-ca.crt  kfp-ca.crt  s3-ca.crt
  manual-deadline.facts.json  manual-fail.facts.json  manual-stop.facts.json  manual-success.facts.json
```

**结论**：仓库里的 `configs/config.yaml`（只有 `server` 段）**不是**实际运行的配置；真正生效的是 `ani-system/md-fixed-config` 的 `config.yaml` 键，手工维护节奏。所以"加 Model 端点配置位"必须**同时**改两处：仓内 conf proto + 部署侧 CM。只改仓库一定不生效——初稿 1.1 的提醒是对的，现在有确切答案了。

同命名空间另有 `modeldev-runtime-config-grpc30-37d6a354-358534c3`（9 键），未挂载到该 Deployment，用途待查。

### 1.3 【修正】ModelDev 部署镜像口径与初稿不符

| 来源 | 值 |
| --- | --- |
| 初稿 call-table §1 | `localhost/ani-modeldev-service@sha256:ba17dc368a34d79baae6a8321757e774459ab3f20d125e87747a74d220e76769` |
| 实测（deployment spec） | `172.16.101.10:5000/ani/ani-modeldev-service:20261014-cursorkey` |
| 实测（pod imageID） | `172.16.101.10:5000/ani/ani-modeldev-service@sha256:7ae434ef979dd88c65fdd23810c81c91626bc8ef43a360513c35143950c5a26b` |

Pod 注解 `ani.io/deployment-run: three-services-20261014-identity`。**两者不是同一个 digest**。开工前必须先钉死"当前运行的 ModelDev 是哪个镜像"——基线错了，后面所有"我改的代码在哪一版上"都会含糊。

### 1.4 【已定】验证宿主就绪度（回答 B11）

远端 `ani-01`（经 `local-remote/ttyd-exec.py`，须带 `--source-ip 192.168.203.132`）实测：

| 项 | 实测 | 结论 |
| --- | --- | --- |
| 连通性 | `hostname=ani-01`，退出 0 | ✅ 可达 |
| kubectl / helm | `v1.35.8` / 有 | ✅ |
| Go | `go1.22.2`（repo 要求 `1.26.9`） | ⚠️ 但见下 |
| **Go 1.26.9 工具链** | `goproxy.cn` 上 `v0.0.1-go1.26.9.linux-amd64` info=200 / zip=206 | ✅ **可自动拉取**（`GOTOOLCHAIN=auto` + `GOPROXY=https://goproxy.cn,direct`） |
| make / gcc | 均有 | ✅ |
| **docker / podman** | **都没有** | ❌ `scripts/with-test-postgres.py` 靠 `docker run postgres@sha256:639ab7ce…`，**PG 级测试跑不了** |
| psql | 不在 PATH | ❌ 同上 |
| 资源 | 16c / 31G / 磁盘余 84G | ✅ 够用 |
| 仓库副本 | **无任何 `ani-*` 仓库 checkout** | 需先 clone/同步 |

**净结论**：**CODE 层（编译、`go vet`、域内单测）现在就能在 ani-01 上跑**，不用等任何人装 Go。**PG/门禁层**差一个 docker——这是一次性安装，属环境 owner 的动作，别再排到"等 Model 回复"后面。

> 本机（Windows）Go 是 `1.25.4`，同样低于要求；但编译验证应放 ani-01（Linux/amd64），与后续镜像构建同平台，避免 Windows 上的假通过。

### 1.5 【复核成立】代码断点确认

| 断点 | 证据 |
| --- | --- |
| 登记仍是桩 | `internal/biz/finetuning_registration.go:43-51` 只 `RecordRegistrationUnavailable` + `return … ErrModelDependencyUnavailable` |
| 生产装配没接 Model reader | `cmd/ani-modeldev-service/command.go:98` 用三参 `biz.NewManagedAdmissionResolver(releases, inputs, facts)`；四参 `NewFinetuningAdmissionResolver`（`internal/biz/finetuning_admission.go:22`）只有测试在用 → `resolver.models == nil` → `finetuning_admission.go:60` 恒 `ErrFinetuningNotEnabled` |
| 全仓无 Model 客户端 | `grep -rn 'model.v1\|model_service' --include=*.go --include=*.mod --include=*.proto ani-modeldev-service/` **零匹配** |
| 存储交接只能受控复制 | `data/lifecycle/finetuning_registration.go:25` 已定业务键 `md-container:<exec>` / `md-<exec>` / `exec-<exec>`，与初稿一致 |
| 目标可写检查不存在 | Model 侧复核结论未变 |

### 1.6 【修正】登记 RPC 的执行位置：在**主服务**，不在 step 组件

链路实测：

```
KFP workflow 的 register Pod  ──> cmd/ani-modeldev-step（component 镜像）
   该组件调用 r.client.RegisterModel(...)        internal/component/finetuning.go:228
   → ModelDevStepService.RegisterModel           （回调主服务，internal/server/managed_step.go:43）
   → internal/service/finetuning_step.go:71      s.runtime.RegisterModel(...)
   → biz.ManagedRuntime.RegisterModel            internal/biz/finetuning_registration.go:31
   → data/lifecycle.RecordRegistrationUnavailable
```

**所以 Model 客户端 + mTLS 材料要加在 `ani-modeldev-service` 主服务（`cmd/ani-modeldev-service`）**，不是在 step 组件里。初稿只写了"ModelDev 侧要加客户端"，没点明落点——这决定了要改哪个 binary、重打哪个镜像（主服务镜像，不是 component 镜像）。

### 1.7 【修正】Q4「超时预算」其实已经冻结在 Release 合同里

`contract/cpup01/finetuning_snapshot.go:134` 已有 `ComponentBudgets.RPCTimeoutSeconds`（`:180` 校验 `>0`），并且在**登记截止时间的计算中已被使用**：

```go
// internal/data/lifecycle/finetuning_registration.go:53,76
content.DeadlineAt = current.now.Add(time.Duration(Budgets.RPCTimeoutSeconds) * time.Second)
```

即"单次 RPC 预算"不是开放问题，是既有冻结合同（conformance fixture 取 30s）。Q4 应改成**确认题**："原 publication 的 RPC 预算是否同样约束 Model 调用；重试是否共享同一预算"。别再当填空题问对方。

---

## 2. 缺口、责任与状态（复核后）

| # | 缺口 | 责任方 | 状态 |
| --- | --- | --- | --- |
| G0 | **Model 当前 CrashLoop（RustFS 自检超时）** | 环境 owner + Model owner | **新发现，阻塞 Step 3** |
| G1 | Model 未登记 ModelDev 身份（policy map + `expectedIdentities` 两处 + 新增 principal 分支） | Model | 阻塞（已问 Q1/Q3） |
| G2 | ModelDev 无 Model 端点：仓内 conf 无字段 **且** 部署 CM `md-fixed-config` 无字段 | ModelDev（本地可写） | 可开工 |
| G3 | `MERGED_HF_MODEL` ↔ Model `format` 枚举映射 | Model | 阻塞（已问 Q2/T6） |
| G4 | D09.3 登记派发仲裁未实现 | ModelDev（本地可写、可单测） | 可开工 |
| G5 | Model API 模块版本与消费方式 | Model 发布 + ModelDev 消费 | 阻塞（已问 Q6/T13） |
| G6 | `ListModelVersions` 未放行 | Model | 阻塞（已问 T12） |
| G7 | "目标可写"检查不存在 | Model | 阻塞（已问 T7） |
| G8 | Model tenant UUID ↔ Gov 租户映射 | Gov + Model | 阻塞（已问 Q5） |
| G9 | **PVC 19/19、380Gi 用满** | 环境 owner | 阻塞 Step 3（与 Model 无关，单独催） |
| G10 | ani-01 无 docker → PG 级测试跑不了 | 环境 owner / 我方 | 可解，一次性安装 |
| G11 | ModelDev 部署镜像口径不清（§1.3） | ModelDev | 开工前钉死 |

**能立刻动的有两块**：G2 + G4（本地实现），G10（环境）。**其余全在等外部**——所以"等回复期间没活干"是错觉。

---

## 3. 执行计划

### Step 0 · 今天，零依赖（并行发出）

| # | 动作 | 产出 |
| --- | --- | --- |
| 0.1 | 发 `finetuning-model-owner-questions.md`，**按 §1.1/§1.7 修订后再发**：补 G0 提问、把 Q4 改成确认题、附 §1.2 的配置来源事实 | 待回填的函 |
| 0.2 | 单独报环境 owner：**G0（Model CrashLoop）+ G9（PVC 容量）+ G10（ani-01 装 docker）** | 三个工单 |
| 0.3 | 钉死 ModelDev 当前运行镜像（§1.3），更正 call-table §1 | 基线表更正 |
| 0.4 | 把 §1.4 的 ani-01 就绪度写进 `finetuning-p0-plan.md` 的 B11（现在说"环境加装中"，实际 CODE 层已可用） | 文档更正 |
| 0.5 | 提交目前全部 `??` 未跟踪文档（含本案） | Git 记录 |

### Step 1 · 本地可写、可单测（不等任何人）

> 关键判断：**没有 Q1/Q2 也能开发并单测**，只是不能标 PASS。别把这些排到"等回复后"。

| # | 动作 | 落点 | 验收口径（仅 CODE） |
| --- | --- | --- | --- |
| 1.1 | 加 Model 端点配置位 | 仓内 conf proto + `cmd/ani-modeldev-service/command.go` 装配 + 部署 CM `md-fixed-config`（§1.2） | 缺配置时**启动即拒**，不静默降级 |
| 1.2 | 定 Model proto 消费方式 | 参照 Gov 先例：`github.com/liangzai006/ani-model-service v0.0.0-20261013092523-8951c85bc150`，import `api/model/v1`，**无 replace**（`ani-governance/go.mod:20`）。若 Model 未发布新 pin，先用窄接口 + 本地替身做消费者合同测试 | 不得复制 Proto、不得 `replace`；替身结果**不算** MR 通过 |
| 1.3 | 接线 `FinetuningModelFactsReader`（C1/C2 只读） | `command.go:98` → 四参 `biz.NewFinetuningAdmissionResolver` | FINETUNING 受理不再恒返 `ErrFinetuningNotEnabled` |
| 1.4 | 实现 D09.3 派发仲裁 | `internal/biz/finetuning_registration.go` + `internal/data/lifecycle/finetuning_registration.go` | 与 Stop/关闭/deadline **共用执行行锁**；事务内重核 spec/阶段/业务键/目标/停止状态/剩余预算；**事务外才做 RPC 与对象 I/O**；区分"意图已存 / 已获准派发 / 结果未明"；`CreateModel` 与 `CreateModelVersion` **分别仲裁** |
| 1.5 | C3–C7 客户端 + 幂等键 | `internal/data/`（新增 model 客户端适配） | 业务键固定 `md-container:<exec>` / `md-<exec>` / `exec-<exec>`；先查证再有限重放；404 不单独证明"原写未发生"；不得把 versioned object 降为 latest |
| 1.6 | 域内单测 | `internal/data/lifecycle/*_test.go`、`internal/data/resolutiontest/` | 同键同内容重放 / 同键异内容拒绝 / Stop 先于派发→该外部写次数 0 / 派发先于 Stop→容器收据保留且新版本写 0 |
| 1.7 | 在 ani-01 建 CODE 门禁 | `GOTOOLCHAIN=auto GOPROXY=https://goproxy.cn,direct` | `go build ./...`、`go vet ./...`、`go test -count=1 ./...` 绿；门禁脚本与退出码留证 |

**出口口径**：只能报 `CODE`。MR01–MR08 与相关 FT 的 Model 断言保持 `NOT_RUN`。

### Step 2 · 等 Model owner 回填（Q1–Q6）

| 谁 | 做什么 |
| --- | --- |
| Model | ① 签发登记 ModelDev 的唯一 DNS SAN；② `policy map` + `expectedIdentities` **两处都加**；③ 新增 principal 构造分支（actor 前缀、workload 取值）；④ `MERGED_HF_MODEL` 的 format 映射；⑤ "目标可写"承载方式；⑥ 放行 `ListModelVersions` 或补按业务键点查；⑦ 确认 bucket/配额；⑧ 发布可消费的模块版本（要能 `go get`，注意 `github.com/liangzai006/…` 的可取性） |
| 环境 owner | G0（RustFS/Model 启动）、G9（PVC 容量）、G10（ani-01 装 docker）、新客户端证书进 secret |
| 双方 | tenant UUID ↔ Gov 租户映射（G8）；确认 RPC 预算语义（§1.7） |

### Step 3 · M1-B 真实联调（硬前置三条）

> 三条缺一不可，且**都不在 Model 代码仓里**：
> ① Model 1/1 Ready 且 `/readyz` 200（G0）；② 新 case 的 20Gi PVC 容量授权（G9）；③ Model 侧身份已签发登记（G1）。

1. 官方 renderer 生成新 execution/op/新 case（不重放历史 case）。
2. 真实训练 → export/verify → collect/publish（M3-A 已验证子链，不重做）。
3. 真实 `CreateModelVersion`，存储走 **C7 受控复制**（`bucketForTenant` 在 `tenantBuckets=false` 时恒返回 `ani-models`，直接引用在代码上不成立）。
4. 读回核原字段 + `READY`，核 tar 的 version/size/SHA 与原 publication 一致。
5. 正常关闭；原业务键重放只返回同一 ModelVersion。

---

## 4. 验收口径（分层，不得越级）

| 层 | 允许的表述 | 不得表述 |
| --- | --- | --- |
| CODE | `go build/vet/test` 绿、单测覆盖 | 不得升为 MR PASS |
| 真实 PG | 在 ani-01 + docker 上重建后另行取证 | 不得沿用 Fedora 的旧门禁结论 |
| 目标集群 | 新 case 的真实 Run/TrainJob/publication/ModelVersion | 不得用旧 case 或替代 fixture 抵扣 |
| 真实用户链 | Gov/BFF 真实来件 | 未接通时不得自称已通过 |
| 独立 Inference | FT29/C23 用**本次登记**的 ModelVersion | M3-A 的离线 forward/generate 不能替代 |

---

## 5. 红线（不要做）

- 不冒用 `ani-governance` 身份，不绕开 Model 授权校验。
- 不复制 Model Proto、不用本地 `replace` 冒充互操作。
- 不重跑历史用例、不借旧 case 改键、不续 deadline、不重置预算。
- 不删/不扩历史 PVC，不改 SQL 状态，不强拆 finalizer。
- **不顺手去"修" RustFS/MWC/VAP/CNI 或共享 Model/Gov/Inference 部署**——G0 报给 owner，别自己动手。
- 不把 `Ready`/`Job Complete`/`Workflow Failed`/`404` 单独当业务通过。

---

## 6. 建议的开工顺序

**今天**：0.1–0.5 全发（都是零依赖）。
**紧接着**：1.1–1.3（配置位 + 接线，改动小、可立即验证编译）+ 1.7（ani-01 门禁骨架）。
**然后**：1.4–1.6（D09.3 仲裁，本步工作量最大、也是唯一"无 Model 也能测透"的核心）。

理由：1.4 的仲裁逻辑不依赖 Model 的任何回复，且 FT25.1–3 的域内部分能完整自测；等 Q1/Q2 回填时，只剩"把 client 换成真的"这一小步。
