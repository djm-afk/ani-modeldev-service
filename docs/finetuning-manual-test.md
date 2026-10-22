# CPU 微调手动验收手册

对应 2026-10-15 已完成的 CORE_FLOW / M3-A。后续开发顺序见[后续工作交接](finetuning-next-handoff.md)，实现概览见[执行主链](finetuning-core-flow.md)，逐次原始记录见[本轮最终交接](../.scratch/finetuning-cpu/records/M3-A-handoff-20261015.md)。

本手册手动启动、观察后端验收驱动。驱动使用真实 ModelDev 业务代码、PostgreSQL、KFP、Trainer、PVC 和 S3；上游来件及只读基模事实由明确的应用层 fixture 提供。Gov/BFF 登录入口、真实 Model 登记、生产 Inference 尚未进入这轮验收。

正常业务顺序是：导入 JSON 并持久 READY → 冻结参数和目标 → 持久受理 → 一个 KFP Run → 准备 Pod 下载到本次 PVC → 一个 TrainJob 挂载同一 PVC 训练 → KFP export/verify → collect/publish → 独立确认 publication → 登记/关闭。当前 Model 未接入，A/B 的合格结果为 **PUBLISHED + MODEL_DEPENDENCY_UNAVAILABLE + CLOSED**，并非 Model READY 或整版业务成功。

## 1. 已做过哪些测试

表中“通过”是既有原始记录的结论；新复测开始时另记 NOT_RUN，不覆盖这些记录。

| 编号 | 用例及本次实际结果 | 原始记录 |
| --- | --- | --- |
| M01 | A：默认 0.0001/20，真实 20 步、export/verify/collect、18 文件及 bundle 发布、关闭和回收 | [A 终态](../.scratch/finetuning-cpu/records/CORE_FLOW-a-f7b852e-20261015/actual-summary-terminal.json) |
| M02 | B：显式 0.0002/10，真实 10 步，导出权重 SHA 与 A 不同，发布及关闭通过 | [B 终态](../.scratch/finetuning-cpu/records/CORE_FLOW-b-f7b852e-20261015/actual-summary-terminal.json) |
| M03 | 脱离训练卷取回 A：20 个固定版本对象核验、离线 CPU forward/generate，通过 | [独立 reader](../.scratch/finetuning-cpu/records/READBACK-a-f7b852e-v3-actual/actual.json) |
| M04 | 训练 writer Running 后 USER_STOP，原训练退出 143，关闭、回收、同键重放通过 | [Stop 原始记录](../.scratch/finetuning-cpu/records/CORE_FLOW-stop-9c8ab03-20261015/original-native.json) |
| M05 | R3：训练运行时同进程 app.Stop → buildApp → app.Run，原执行继续发布、关闭和回收 | [R3 原始记录](../.scratch/finetuning-cpu/records/CORE_FLOW-restart-1ca8f03-r3-20261015/original-native.json) |
| M06 | Late：原后台 Pod 退出，新 Pod/进程接手原命令；训练完成后再 Stop 的回收通过 | [Late 终态](../.scratch/finetuning-cpu/records/CORE_FLOW-late-stop-1ca8f03-r2-20261015/actual-summary-terminal.json) |
| M07 | Deadline r2：到期前已实际训练，原 deadline 触发，退出 143；无 export/verify 新许可、无发布，关闭和回收通过 | [Deadline 原始记录](../.scratch/finetuning-cpu/records/CORE_FLOW-deadline-1ca8f03-r2-20261015/original-native.json) |
| M08 | 正式 webhook：两副本严格 TLS；依赖临时失败返回 ServiceUnavailable，原 Argo node Pending 后自动恢复 | [rollout](../.scratch/finetuning-cpu/records/ENV-webhook-848d50b-20261015/rollout-actual.json)、[原 Pending WF](../.scratch/finetuning-cpu/records/CORE_FLOW-restart-1ca8f03-r2-20261015/original-workflow-transient-pending.json) |
| M09 | 参数反例、并发受理、原键重放、CPU 合同回归、清理异常分支：CODE/真实 PG 验证通过 | [门禁及具名测试](../.scratch/finetuning-cpu/records/M3-A-handoff-20261015.md#参数反例与db仲裁门禁) |

M05 的后台 Pod 和 OS 进程保持不变；真正的新 Pod/进程恢复由 M06 证明。M09 不是目标集群负向 API 或真实 Gov/BFF 验收。

## 2. 环境、材料与执行位置

本机仅阅读/编辑文档和 Git。以下 Python、kubectl、测试及负载命令都在 `ssh fedora` 后执行。这里复用保留的任务环境，不重新安装共享组件。

```bash
ssh fedora
```

在 Fedora 终端设置：

```bash
set -euo pipefail
umask 077
export FT_BASE=/home/chabking/workspace/finetuning-cpu-m3a-20261014
export FT_ROOT="$FT_BASE/env/deployment-direct-s15-1ca8f03-r2"
export FT_SOURCE="$FT_BASE/workload/assets-driver-1ca8f03/source"
export FT_RUN="$FT_SOURCE/scripts/finetuning-core-flow/run.py"
export FT_KUBECTL="$FT_BASE/env/bin/kubectl"
export FT_KUBECONFIG="$FT_BASE/env/.private/kubeconfig-ani-lab-minified"
export FT_TENANT=ani-ft-core-20261014
export FT_SYSTEM=ani-ft-core-system-20261014
export FT_SOURCE_SHA=1ca8f031dab70cfd702edb9c277ca6de8c0897db
export FT_DRIVER_IMAGE=localhost/ani-ft-core-driver@sha256:9be4c9ab0daed5beccbb0ad2988f94c60c7b7a2fd138f86274ef96c7b0c3c0a4
export FT_COMPONENT_IMAGE=localhost/ani-ft-core-component@sha256:39cde89462cbc215cbb480f662a2f2f1870abbbe6768b955325b06dc60db762a
function k() { "$FT_KUBECTL" --kubeconfig "$FT_KUBECONFIG" --request-timeout=30s "$@"; }
test -s "$FT_ROOT/.private/driver-state.json"
test -s "$FT_ROOT/candidate-release.json"
test "$(git -C "$FT_SOURCE" rev-parse HEAD)" = "$FT_SOURCE_SHA"
test "$(k get ns kube-system -o jsonpath='{.metadata.uid}')" = 5277649d-e28d-4a0a-ae76-8354365c63bc
test "$(k get ns "$FT_TENANT" -o jsonpath='{.metadata.uid}')" = 8f79e7cc-769b-4b2d-87d3-a69c53c946f3
test "$(k get ns "$FT_SYSTEM" -o jsonpath='{.metadata.uid}')" = f81b058f-91fb-4599-b96e-b700c605cc92
k config view --minify -o jsonpath='{.clusters[0].cluster.server}'
k get nodes -o wide
sha256sum "$FT_ROOT/candidate-release.json"
```

预期 API 为 `https://172.16.101.10:6443`，节点 InternalIP 仅 `.10/.11/.12`；Release 文件 SHA 为 `1191b7d5e2013b2ebc6a197c88b1fc56de6d6304f82e57b80305d227367c106f`。该候选仍 disabled；此测试无需启用公共 FINETUNING。

最终 Git 基线为 `848d50bd5878454ea0d69096dcaa2778365460b9`；业务驱动仍为上面的 1ca，组件为 f7。正式 webhook 为 848、镜像 digest `1e36436e41f15aff9bda4b73737e9f0d24077cdb27ae04097f0d2c565d51b840`。PipelineVersion 为 `f07dcf97-14d4-4cfb-a807-cdc0913c8a54`，IR SHA 为 `3d1d3a85de4474718d864428a28f0b53fac9ea0bbe0582b8c0fce24ad57c3f8d`。换源码、镜像、环境或 Release 时，须建立新绑定和本轮记录，不能沿用本表的通过结论。

`.scratch/`、Fedora 原始材料和 `.private/` 不随 Git clone 分发。私有目录有数据库连接、证书和有限 S3 身份，只引用文件路径，不打印内容、不提交 Git。材料缺失时先恢复保留任务包。

### 每个新用例启动前

```bash
k get deploy ani-modeldev-webhook -n "$FT_SYSTEM"
k get endpointslice -n "$FT_SYSTEM" -l kubernetes.io/service-name=ani-modeldev-webhook
k get role ani-modeldev-webhook -n "$FT_TENANT" -o yaml
k get resourcequota core-flow-budget -n "$FT_TENANT" -o json
k get pvc -n "$FT_TENANT" -o wide
k get pods -n "$FT_SYSTEM" -l app=core-flow-driver -o wide
```

必须有两副本 webhook Ready、原 Role 已恢复，且没有 Pending/Running/Unknown 的旧业务驱动。逐用例串行运行：这些驱动共用 `modeldev` Service 和原环境绑定。

每个新 execution 需要一个新 20Gi PVC，并会保留。**最终快照已经使用 19/19 个 PVC、380Gi 请求配额；原环境不能直接再创建新用例。**先由环境 owner 确认容量和本次有限配额，再开始复测。本手册不扩配额、不删除旧 PVC/S3，不重放 TrainJob/VAP、RustFS 或共享 CNI 的历史补丁。19 个卷均保留的依据见[最终环境](../.scratch/finetuning-cpu/records/FINAL-environment-actual-20261015.json)。

## 3. 公共操作：新用例、观察和取证

每次手工选择一个从未出现过的 case 名；以下名字只是新复测示例。先为它建独立记录目录：

```bash
export FT_SCENARIO=a
export FT_CASE=core-flow-a-manual-20261015-01
export FT_RECORDS="$FT_BASE/manual-tests/$FT_CASE"
mkdir -m 700 -p "$FT_BASE/manual-tests"
mkdir -m 700 "$FT_RECORDS"
```

先在另一个终端重复第 2 节变量设置，并设置相同的 `FT_CASE`、`FT_RECORDS`（沿用已创建目录），保存 Workflow 原始 watch，再 render/start。它只读集群，不阻挡自动回收。保留脚本与本次实测相同，只更换输出目录，仍为 1800 秒/64MiB 上限：

```bash
export FT_WATCH_OUTPUT="$FT_RECORDS/workflow-watch"
python3 - <<'PY'
import hashlib,os
from pathlib import Path
root=Path(os.environ['FT_ROOT']); records=Path(os.environ['FT_RECORDS'])
p=root/'watch-workflows-prestart-v2.py'; raw=p.read_bytes()
assert hashlib.sha256(raw).hexdigest()=='f71ff5d9bec0358ff34d286263b89c54a22df7450e5d56fc4a24a64dff8fca8a'
old="R = B / 'env/deployment-direct-s15-1ca8f03-r2/workflow-watch-prestart-v2'"
s=raw.decode(); assert s.count(old)==1
with (records/'watch-workflows.py').open('x') as out:
    out.write(s.replace(old,"R = Path(os.environ['FT_WATCH_OUTPUT'])"))
PY
python3 -u "$FT_RECORDS/watch-workflows.py"
```

业务启动终端检查 `$FT_RECORDS/workflow-watch/ready.json` 的原 namespace UID、非空 RV 和实际 child PID/cmdline，确认 child 存活、stderr 空、尚无结束 receipt。脚本会保存原始事件及结束 receipt/SHA。若达到容量上限或最终事件截断，要如实记下缺口。

其底层原生接口如下，便于诊断或从最后 RV 续接；默认使用上面的有界脚本，不同时启动重复 watch：

```bash
FT_WF_API="/apis/argoproj.io/v1alpha1/namespaces/$FT_TENANT/workflows"
k get --raw "$FT_WF_API" > "$FT_RECORDS/workflows-initial.json"
FT_WF_RV=$(python3 -c 'import json,sys; x=json.load(open(sys.argv[1])); v=x["metadata"]["resourceVersion"]; assert v.isdigit(); print(v)' "$FT_RECORDS/workflows-initial.json")
k --request-timeout=1800s get --raw "$FT_WF_API?watch=true&resourceVersion=$FT_WF_RV&timeoutSeconds=1790&allowWatchBookmarks=true" \
  > "$FT_RECORDS/workflows.raw.jsonstream" 2> "$FT_RECORDS/workflows.stderr.log"
```

使用原生 `--raw` query；这里没有 `kubectl get --resource-version` 参数。确认 watch 实际存活、stderr 为空。正常用例通常可被这一轮 30 分钟 watch 覆盖；超时需从最后实际 RV 续接并另存，不覆盖原流。结束后按原 execution → RunID → Workflow UID 筛选；不要把其他旧 Workflow 的节点混入。

### Render → 审查 → Start（M01/M02/M04/M05）

```bash
python3 "$FT_RUN" render \
  --root "$FT_ROOT" --kubectl "$FT_KUBECTL" --kubeconfig "$FT_KUBECONFIG" \
  --source-commit "$FT_SOURCE_SHA" --driver-image "$FT_DRIVER_IMAGE" \
  --component-image "$FT_COMPONENT_IMAGE" --release "$FT_ROOT/candidate-release.json" \
  --scenario "$FT_SCENARIO" --case-name "$FT_CASE"
```

Render 读取现有绑定、请求短期 Token、做 server dry-run，并冻结新 execution/op 和清单；尚未创建业务 Run。读取安全摘要及清单 SHA，不输出私有 fixture 全文：

```bash
python3 - <<'PY'
import hashlib,json,os
from pathlib import Path
r=Path(os.environ['FT_ROOT']); case=os.environ['FT_CASE']
x=json.loads((r/'.private/driver-state.json').read_bytes())['scenarios'][case]
p=Path(x['manifest_reference'])
assert hashlib.sha256(p.read_bytes()).hexdigest()==x['manifest_sha256']
print(json.dumps({k:x[k] for k in ('phase','execution_id','operation_id','manifest_reference','manifest_sha256','source_commit','release_digest')},indent=2))
PY
```

核对 scenario、原环境、镜像及 Release 后，**只调用一次**：

```bash
python3 "$FT_RUN" start \
  --root "$FT_ROOT" --kubectl "$FT_KUBECTL" --kubeconfig "$FT_KUBECONFIG" \
  --scenario "$FT_SCENARIO" --case-name "$FT_CASE" \
  > "$FT_RECORDS/start.json" 2> "$FT_RECORDS/start.stderr.log"
k get job "$FT_CASE" -n "$FT_SYSTEM" -o json > "$FT_RECORDS/original-driver-job.json"
k logs -n "$FT_SYSTEM" "job/$FT_CASE" -c driver -f
```

原 `JOB_CREATE_PENDING`、超时、断线或已有 Job 都先查原 ID/UID；不要再次 start、删台账或换键制造“重试”。关闭日志跟随窗口只停止观察，不会 Stop 业务。

运行时，可重复调用本轮保留的只读 observer：

```bash
python3 -B "$FT_BASE/workload/direct-s15/observer_v2.py" --root "$FT_ROOT" --case "$FT_CASE"
```

它保存原身份、runtime、首失败 Pod/日志等，位于 `$FT_ROOT/.private/observer-v2/$FT_CASE/`。关键转换每 12～15 秒观察一次，稳定阶段约 30 秒一次。若 observer 遇日志 GET 失败，另查原 Pod UID/状态；这不是业务失败的直接证明。Workflow/步骤 Pod 会自动回收，应在回收前保留原始事件。

最终同时检查 Job/driver 退出码、原 native JSON、StepExitCleanup 和 ComputeCleanup。native 文件在 SYSTEM 的 evidence PVC 中；通过有限只读 metadata helper 挂载该证据卷取回。

新 case 的 producer 路径为 `/evidence/<FT_CASE>.json`；close-only 为 `/evidence/close-only-<execution_id>.json`。原 A 的 helper 清单模板为 `$FT_BASE/workload/evidence-a-f7b852e-fetch/job.json`，同目录 `ready.json` 留存原创建 argv。新 helper 须固定新 producer 文件/exec/op/source/scenario、独立 Job 名，保留只读 evidence PVC、无 API token/Secret/训练卷及 regular-file/O_NOFOLLOW/2MiB 限制，server dry-run 审查后创建一次；将原日志字节写为新记录目录的 `original-native.json`，保存 helper UID/退出码/字节数/SHA。它不补写 SQL，不把查询结果拼成“原 native”。

## 4. M01/M02：默认与非默认真实主链

分别使用第 3 节，选择下表值，每轮结束并确认旧 owner 退出后再测下一轮。

| 场景 | FT_SCENARIO | 新 FT_CASE 示例 | 实际参数来源 |
| --- | --- | --- | --- |
| A | `a` | `core-flow-a-manual-20261015-01` | 原意图 `finetuning={}`，Release 补齐 0.0001/20 |
| B | `b` | `core-flow-b-manual-20261015-01` | 原意图显式 0.0002/10 |

合格条件：

1. 原 execution/op、spec、Snapshot、实际 effective 配置一致；真实 JSON 导入 READY，每轮唯一 Run/TrainJob。
2. prepare 成功后才创建训练；训练读同一 PVC 的只读输入，真实 optimizer_steps 分别为 20/10。参数字段或日志行数不能代替真实步数。
3. export、verify、collect 真实退出 0；publication 独立确认 18 个文件和 bundle 的固定 VersionID/size/SHA。B 实际 merged 权重 SHA 与 A 不同。
4. 当前登记准确返回 `MODEL_DEPENDENCY_UNAVAILABLE`，以 STEP_FAILED 关闭；原 Workflow 的登记组件失败是本轮已声明边界。driver 必须完成自身断言后 exit0。
5. CLOSED 后 StepExitCleanup 完成、ComputeCleanup APPLIED；原计算 UID 独立 GET 为 NotFound，原 PVC 同 UID Bound 保留。原键重放保持 execution/spec/deadline/ClosedAt，不重复计算。

本轮 A：execution `7ebc5706-2bb9-4254-958c-d6e40d3a669b`，publication `10220103-efd0-571f-a5c6-a5e4d57f0886`；B：execution `53c01c4e-4d60-42da-8a9e-788418a81d19`，publication `464d2798-484b-591a-9f27-29ec90da69e4`。

## 5. M03：脱离训练卷取回、校验并加载

本轮 A 的 bundle 是 1,992,053,248 字节，SHA `9a90d00223fcd6e096f8463172265c88d5979a3751e707815cbc7c5b4666107d`。读取[原 reader actual](../.scratch/finetuning-cpu/records/READBACK-a-f7b852e-v3-actual/actual.json)和 Fedora 材料：

```bash
cat "$FT_BASE/workload/readback-a-f7b852e-v3/review.json"
cat "$FT_BASE/workload/readback-a-f7b852e-v3/receipt.json"
sha256sum "$FT_BASE/workload/readback-s15-v3/readback.py"
```

新复测须先有新执行的原 native/publication。以下使用第 3 节取回的新 A 原 native，生成新 reader 候选：

```bash
export FT_NEW_NATIVE="$FT_RECORDS/original-native.json"
export FT_READER_ROOT="$FT_BASE/manual-tests/$FT_CASE-reader"
export FT_READER_JOB="$FT_BASE/workload/readback-s15-v3/job.py"
export FT_READER_IMAGE=localhost/ani-ft-core-train@sha256:65b4756c2663b12ce4604452f43f628c519e50ea839fb7a56e578ed074d55591
test -s "$FT_NEW_NATIVE"
test ! -e "$FT_READER_ROOT"
python3 "$FT_READER_JOB" render --root "$FT_READER_ROOT" \
  --driver "$FT_NEW_NATIVE" --image "$FT_READER_IMAGE" \
  --deploy-script "$FT_SOURCE/scripts/finetuning-core-flow/deploy.py" \
  --ca "$FT_BASE/env/deployment/.private/s3-ca.crt" \
  --credentials "$FT_BASE/env/deployment/.private/readback.json"
```

审查新 `manifest.json`、`receipt.json`：原 exec/publication 固定，镜像必须匹配 native Snapshot program，凭据为获准有限只读身份。审查后使用相同 flags 将 `render` 改为 `apply`，只执行一次；它会创建有限 reader 身份及 Job。随后观察：

```bash
python3 "$FT_READER_JOB" observe --root "$FT_READER_ROOT" \
  --deploy-script "$FT_SOURCE/scripts/finetuning-core-flow/deploy.py"
```

同一 execution 的 reader Job 名由原 execution 派生；已有原 Job 时 observe 它，不重新创建。原 `readback-a-f7b852e-v3` 清单、Job 名和台账已执行，不能再 create。凭据仅引用文件路径，不打印内容。

必须检查：不挂训练 PVC/hostPath/API token/控制身份；仅已有有限 GetObject/GetObjectVersion 身份；按原 VersionID 真实 HEAD/GET 18 文件+manifest+bundle 共 20 对象；实际大小/SHA、严格 USTAR 内 10 个 HF 文件及 Adapter/metrics/summary 的执行关联一致；离线 CPU forward 有限 logits、实际 generate，main/init 均 exit0。投影文件通过 init 物化为 regular 只读文件，不放宽 reader 的 O_NOFOLLOW 等检查。

本次只证明已发布产物可独立消费，不证明 Model READY 或 FT29 生产推理。

## 6. M04/M05：运行中 Stop 与后台应用重建

仍用第 3 节的官方 render/start：

| 用例 | FT_SCENARIO | 新 FT_CASE 示例 | 自动触发点 |
| --- | --- | --- | --- |
| Stop | `stop` | `core-flow-stop-manual-20261015-01` | 原训练 Pod 的 node 容器实际 Running 后调用正式 ApplyCloseIntent，并立即重放相同 Stop |
| 应用重建 | `restart` | `core-flow-restart-manual-20261015-01` | 同一 writer 运行后 app.Stop → 等退出 → buildApp → app.Run |

Stop 合格：`StartedAt ≤ ObservedAt < StopRequestedAt`；USER_STOP generation1；实际 node 退出证据、CLOSED、Step/Compute 回收和原键重放完整，无新 export/verify 许可或 publication。本轮原 node exit143；日志归档计数准确为 0，不补造归档。

应用重建合格：原 execution/spec/deadline/Run/唯一 TrainJob 保持，重建后仍自动训练、导出、验证、发布、关闭和回收，driver exit0。原 R3 训练 20 步、Step Pod 留证及回收 21 个、Compute APPLIED1、日志归档 1。此模式不删除原后台 Pod；不得把它写成“杀 Pod 恢复”。

## 7. M06：新 Pod/进程接手，训练完成后停止并回收

这项采用故障注入：后台 owner 在训练 Running 时退出，原训练自然完成，然后新进程接收原命令的 Stop 并收尾。原 A 驱动被中断，不能以正常 A 的 PASS 验收；检查的是独立 close-only producer。

本轮有限工具：`$FT_BASE/workload/close-late-stop-1ca8f03-r2/prepare.py`，原计划与精确 UID 见该目录 `plan-review.json`、`suspend-review.json`、`ready.json`。它们固定了旧 case/execution/输出目录，**以下为操作接口说明；为新 case 先生成并审查新绑定版本，不能执行旧脚本重放旧命令。**

1. 按 A 启动新 case。在实际训练 Running 且持久 Observation=RUNNING 时记录原 driver Job/Pod 和 TrainJob/JobSet/Job/训练 Pod UID、node ContainerID。
2. 用该 case 的 `prepare.py suspend-review` 传入四原 UID，生成 Job UID/RV/fullspec CAS patch；server dry-run 并审查。只将原后台 Job `spec.suspend=true`，不停止训练、不改 Service/数据库。确认原后台 Pod 缺失。
3. 原训练自然完成；保存原 node exit0、TrainJob Complete/JobSet Completed 原始时间。旧后台已停，持久 Observation 仍保留原 RUNNING；没有先前 Stop/Publication/CLOSED。
4. 启动原 TrainJob/JobSet 的只读 watch，再运行 `close-review`（三个 UID），冻结同 execution/op/spec/acceptedAt/deadline/绑定的 close-only ConfigMap+Job。StopRequestedAt 必须严格晚于原 Completed。
5. 审查 helper `app=core-flow-driver`、原 binding/镜像、只挂 evidence PVC、不挂训练卷。按清单 items 顺序各 create 一次：ConfigMap → Job，逐次保存结果/UID；结果不明先核原对象，不重发。
6. 核 helper 使用新的 Pod/进程，调用既有 ApplyCloseIntent/Stop replay 后启动生产后台；不重新 Accept、不新建 Run/TrainJob、不写 DB 状态。

四 UID 接口：

```text
python3 <新case的prepare.py> suspend-review \
  --expected-driver-job-uid <原driverJobUID> \
  --expected-trainjob-uid <原TrainJobUID> \
  --expected-training-pod-uid <原训练PodUID> \
  --expected-driver-pod-uid <原driverPodUID>
python3 <新case的prepare.py> close-review \
  --expected-driver-job-uid <原driverJobUID> \
  --expected-trainjob-uid <原TrainJobUID> \
  --expected-training-pod-uid <原训练PodUID>
python3 <新case的watch-original-controllers.py> \
  --trainjob-uid <原TrainJobUID> --jobset-uid <原JobSetUID> \
  --job-uid <原训练JobUID> --pod-uid <原训练PodUID>
```

watch 需实际收到同 UID 的初始事件、child 存活且 stderr 为空。原一次 watch 曾正常结束，后续从最后实际 RV 用原生 `get --raw ...?watch=true&resourceVersion=...` 重放续接；不能仅凭 started.json 或 404 推断分支通过。

新包的 `suspend-forward.jsonpatch` 经审查后用 `k patch job "$FT_CASE" -n "$FT_SYSTEM" --type=json --patch-file <新patch文件> -o json` 精确应用一次。新 close-only 清单同样要核 SHA，按 items 顺序分别 `k create -f - -o json`；每项成功响应保存原 UID 后才进行下一项，原单次创建台账见 `$FT_BASE/workload/close-late-stop-1ca8f03-r2/root-closehelper-create-once-ledger.json`。上面尖括号段是需重新绑定的接口，不是可直接复制执行的历史命令。

合格关键原始事件：原 TrainJob generation2/spec.suspend=true，报告 post-Stop Suspended；原 JobSet generation2/spec.suspend=true，保留 pre-Stop Completed 且无 Suspended 条件；随后原 UID 删除。再核 close-only native exit0、USER_STOP CLOSED、StepCleanup 完成、Compute APPLIED1、五原 UID 独立 NotFound、原 PVC Bound 保留。

本轮 execution `b4031b61-9b8e-4ef4-98fa-f37417d8c227`：训练 Complete `09:14:51Z` < Stop `09:15:16.683327Z`；新 helper Pod `0424ff64-b4fc-46b8-beab-597534395abe` exit0，11 个步骤 Pod 留证清理，原训练 20 步日志归档完整。以上原时间均为 UTC。

## 8. M07：训练中原 deadline 到期

本轮 r2 先实际训练并产生 metrics，再由原 deadline 停止。原 Release 总预算仍为 10800 秒；测试通过官方 renderer 回溯 acceptedAt，构造“剩余 240 秒的已受理执行”，没有修改运行中快照或续期。240 秒来自当轮实测约 190 秒出现 writer、约 267 秒训练退出；材料/环境改变后须重新测量。

历史 r1 把 render/start 分开约 93 秒，结果在训练前到期，driver1；它只证明准备阶段到期关闭，未抵扣本项。本轮 r2 在同一 Fedora Python 进程连续 render→审查→start，间隔 2.800076 秒。原固定入口 `$FT_ROOT/deadline-r2-preparation/atomic-deadline-r2-once.py` 已执行且有独占台账，不能原样重跑。

新复测先完成容量/唯一 owner/原始 watch 前提，选择新 case，例如：

```bash
export FT_SCENARIO=deadline
export FT_CASE=core-flow-deadline-manual-20261015-01
export FT_RECORDS="$FT_BASE/manual-tests/$FT_CASE"
mkdir -m 700 "$FT_RECORDS"
```

新 case 的连续启动模板如下，保持本次已验证的机制；这是新操作模板，未在写本文时再启动业务。先在另一终端按第 3 节启动该 case 的 watch，再执行整段，中途不暂停等待人工/模型回复：

```bash
python3 - <<'PY'
import datetime,hashlib,json,os,subprocess,sys,time
from pathlib import Path
root=Path(os.environ['FT_ROOT']); source=Path(os.environ['FT_SOURCE'])
case=os.environ['FT_CASE']; records=Path(os.environ['FT_RECORDS'])
assert case.startswith('core-flow-deadline-')
sys.path.insert(0,str(source/'scripts/finetuning-core-flow'))
from deploy import Cluster,SYSTEM,TENANT
c=Cluster(os.environ['FT_KUBECTL'],os.environ['FT_KUBECONFIG']); c.guard()
state=root/'.private/driver-state.json'
assert case not in json.loads(state.read_bytes())['scenarios']
def preflight():
    q=c.get('resourcequota','core-flow-budget',TENANT)
    assert int(q['status']['used']['persistentvolumeclaims']) < int(q['spec']['hard']['persistentvolumeclaims'])
    pods=json.loads(c.run(['get','pods','-n',SYSTEM,'-l','app=core-flow-driver','-o','json']))['items']
    assert not any(p.get('status',{}).get('phase') in ('Pending','Running','Unknown') for p in pods)
preflight()
common=['--root',str(root),'--kubectl',os.environ['FT_KUBECTL'],'--kubeconfig',os.environ['FT_KUBECONFIG'],
        '--scenario','deadline','--case-name',case]
render=['python3',os.environ['FT_RUN'],'render',*common,'--source-commit',os.environ['FT_SOURCE_SHA'],
        '--driver-image',os.environ['FT_DRIVER_IMAGE'],'--component-image',os.environ['FT_COMPONENT_IMAGE'],
        '--release',str(root/'candidate-release.json'),'--remaining-seconds','240']
begin=time.monotonic()
subprocess.run(render,check=True)
x=json.loads(state.read_bytes())['scenarios'][case]
p=Path(x['manifest_reference']); assert hashlib.sha256(p.read_bytes()).hexdigest()==x['manifest_sha256']
rows=json.loads(p.read_bytes())['items']; assert len(rows)==9
f=json.loads(next(r for r in rows if r['kind']=='ConfigMap' and r['metadata']['name']==case)['data']['fixture.json'])
assert f['expect_deadline'] and not f['restart_when_training'] and not f['stop_when_training']
assert f['canonical_release']['execution_timeout_seconds']==10800 and f['source_commit']==os.environ['FT_SOURCE_SHA']
deadline=datetime.datetime.fromisoformat(f['accepted_at'].replace('Z','+00:00'))+datetime.timedelta(seconds=10800)
preflight()
now=datetime.datetime.now(datetime.timezone.utc)
elapsed=(now-(deadline-datetime.timedelta(seconds=240))).total_seconds()
assert 0 <= elapsed <= 15 and time.monotonic()-begin <= 15,'超窗：保留材料，停止，不改deadline或自动rerender'
with (records/'start-once.json').open('x') as out:
    json.dump({'execution_id':x['execution_id'],'operation_id':x['operation_id'],
               'deadline_at':deadline.isoformat(),'render_to_start_seconds':elapsed,'phase':'START_PENDING_ONCE'},out)
subprocess.run(['python3',os.environ['FT_RUN'],'start',*common],check=True)
PY
```

独立核对原始时间：实际 `StartedAt ≤ training_observed_at < deadline_at ≤ CloseRequestedAt < ClosedAt`；冻结每台主机的原时间，不用跨机采集时间代替 native 时间。训练必须真实出现 loss/metrics；只有 Pod Pending 或准备成功不够。

本轮 r2：writer observed `09:42:14.858480Z` < deadline `09:43:11.274660Z` ≤ CloseRequested `09:43:11.640417Z`；原 node 退出143，DEADLINE CLOSED，11 步骤 Pod 留证回收，Compute APPLIED1、归档0。native 检查 `FinetuningSteps` 为空、无 publication，原键 replay 保持身份/spec/deadline/OwnerRevision。计算收据有4个 target，另独立 GET 原训练 Pod，合计五原 UID 缺失；PVC 保留。

## 9. M08：正式 webhook 与临时依赖错误

先查当前服务和[正式 webhook 运维说明](modeldev-webhook.md)：

```bash
k get deploy ani-modeldev-webhook -n "$FT_SYSTEM" -o json
k get endpointslice -n "$FT_SYSTEM" -l kubernetes.io/service-name=ani-modeldev-webhook -o json
k get mutatingwebhookconfiguration ani-modeldev-webhook -o json
k get role ani-modeldev-webhook -n "$FT_TENANT" -o json
```

本次实际做过：逐副本校验证书 CA/SAN/严格 TLS 和 ready200；匹配目标 namespace 的真实 KFP Pod 得到精确挂载/权限/磁盘限制/留证标记；20 次 server dry-run 的 unknown_profile 仍拒绝，未创建 Pod。详见[20 次 dry-run](../.scratch/finetuning-cpu/records/ENV-restart-1ca8f03-serverdryrun-20-actual-20261015.json)。

临时故障演练本次只改了本任务的 Workflow GET Role：用 UID/RV/fullrules CAS 暂时限制到旧 WF 名，令新 Run 的原 node 被正式 hook 拒绝，API 识别为 ServiceUnavailable；恢复原 rules 后，同 Run/WF/node 从 Pending 到 Succeeded，无新 Retry node。最终 Role 已完全恢复。记录见[恢复 actual](../.scratch/finetuning-cpu/records/ENV-webhook-role-restored-20261015.json)及 M08 原 WF。

新故障演练应重新封存当前 Role 原 rules、精确 patch/rollback，安排在新 Run 创建首个 Pod 前，server dry-run 后有限执行并及时恢复。旧 forward/rollback 的 UID/RV 和旧 WF 名不能重放，也不能借机改共享 Role/MWC。安全拒绝仍是 403；只有内部依赖不可用为合法 AdmissionReview：外层 HTTP200、Allowed=false、Result.Code503/Reason ServiceUnavailable。核原 node 实际 Pending→Succeeded，不能只凭 HTTP 503 或新建一个成功 Run 代替原节点恢复。

原重启 r2 随后发生过 Argo wait 访问 API VIP 的 `no route to host`；故障原 Pod/WF 已留存，后置 16 次 TLS200 只证明采样连通，根因 UNKNOWN。原记录仍为失败，不由 R3/Deadline 后续通过改判。旧 `ani-system/ani-modeldev-exit-retention` selector 与正式服务不相交，不用它的重启次数解释当前故障。

## 10. M09：参数、幂等与清理的代码／真实 PG 回归

以下是已执行过的验证层级和可手动调用的入口。真实 PG 与外部 Kubernetes/KFP HTTPS 替身的边界见[具名门禁](../.scratch/finetuning-cpu/records/M3-A-handoff-20261015.md#参数反例与db仲裁门禁)。它们不替代 M01～M08，也不宣称新增了真实 Gov/BFF 测试。

在 Fedora 的最终 848 源目录执行；若版本已变化，先核其 HEAD 和干净状态：

```bash
cd "$FT_BASE/webhook/source-848d50b"
git rev-parse HEAD
git status --short
export GOMODCACHE="$FT_BASE/integration/gomodcache"
export GOCACHE="$FT_BASE/webhook/gocache"
export GOMAXPROCS=2 GOFLAGS=-p=2 GOMEMLIMIT=1536MiB
export GOTOOLCHAIN=go1.26.9 GOWORK=off GOPROXY=https://goproxy.cn,direct
go test -count=1 ./contract/cpup01 ./internal/server/modeldevwebhook
python3 scripts/with-test-postgres.py go test -count=1 \
  ./internal/data/resolutiontest ./internal/data/execution ./internal/data/submittest ./internal/data/runtimeproof
```

核对：缺失/null finetuning、非法类型/数字、未知字段、固定项覆盖、A/B交叉组合在计算前拒绝；`{}` 与显式默认保留不同原意图；同键并发唯一赢家、相同收据、重启 pending 不重复 CreateRun；旧 CPU 合同回归保持。未知 writer、虚拟拒绝节点和训练完成后 Suspend 等清理分支均按原真实退出/UID边界断言。

上述定向命令是按现有测试接口提供的手动入口；具名历史命令/退出码以原 receipt 为准。提交门禁本轮已执行并通过：

```bash
python3 scripts/with-test-postgres.py make verify \
  TOOLS_DIR="$FT_BASE/webhook/release-code-0252e8d/.tools/bin"
```

只读查看历史证据、修改本文或整理记录，无须重跑全仓业务门禁。

## 11. 特殊一次性工具与结果归档

M03/M06/M07 的保留工具是本次有限验收材料，不是支持任意环境的产品 CLI。为新 case 复演时，保存新清单、工具 SHA、审阅差异、单次台账和原业务键；原接口有固定 case/ID 的先受控生成新版本。新版本未经实际运行，不能标 LIVE PASS。

每个结果最少保存：源码/镜像/IR/Release 摘要；原 execution/op/spec/acceptedAt/deadline；Run/WF/TJ/JS/Job/Pod/ContainerID；实际步数与退出；publication 固定版本；CLOSED/replay、Step/Compute 收据；五原 UID 的独立 NotFound 与 PVC 同 UID 保留；原 native 字节大小/SHA/取回来源。私有输入与原始全量对象留在私有目录，安全摘要和恢复指针留在 records。

`Ready`、Job Complete、Workflow Failed、TrainJob Complete、Pod 不存在均不能单独判定业务通过。按具体用例核完整断言；结果不明写 UNKNOWN/NEEDS_REVIEW，不补造成功。

历史 a16 的 ComputeCleanup `BLOCKED / attempts=3`、b7 的 `TRAINJOB_CREATE_UNRESOLVED` 仍单列保留；不重置预算、不改 SQL 状态、不强拆 finalizer、不删除原 PVC/产物。后续正式登记/真实前端测试按[后续工作交接](finetuning-next-handoff.md)推进。
