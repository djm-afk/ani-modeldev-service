# KFP / controller 上游源码核对（SOURCE）

采集于 2026-10-31 UTC；精确时刻、HTTP GET、URL、HTTP 状态、内容 SHA256、逐请求退出状态及短暂网络错误见 [AN-controller-matrix.json](AN-controller-matrix.json) 的 upstream_source_provenance。研究仅访问官方公开来源；未连接集群、读取凭据、创建 review、调用现场 KFP、下载镜像或执行构建。以下全是版本限定 SOURCE，不能据此标记现场 PASS。

## 固定版本

| 项目 | tag | 官方 API 解析 commit |
|---|---|---|
| KFP | 2.16.0 | e4ebca310f404dac306e16bc880de12bbbf63b95 |
| Argo Workflows | v3.7.3 | bded09fe4abd37cb98d7fc81b4c14a6f5034e9ab |
| Kubeflow Trainer | v2.1.0 | 73c9bece741ce17d2124abda3ec6bfcf5b5b8e87 |
| JobSet | v0.10.1 | da72d32bb0060354aa04b1a5868f3b363b088446 |

## K03：配置、healthz 与默认镜像命令

KFP 的 `-config` 虽描述为 JSON path，真实代码把值交给 `AddConfigPath`，搜索该目录下 basename 为 `config` 的文件。`AutomaticEnv` 与 `AllowEmptyEnv(true)` 开启，点号映射下划线；显式空 env 有效。配置文件缺失会启动失败，并监听文件变更。[main.go L75, L494–515](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/main.go#L494-L515)

本版本锁定 Viper v1.20.1。相关配置没有绑定同名命令行 flag 时，有效非敏感键的解释顺序为 env → config 文件 → KFP getter 默认值；不能因 Deployment 无 env 就采用默认。Viper 通用顺序还允许显式 Set / 已绑定并改动的 flag 优先于 env，本次 KFP 初始化的这些键未发现这种设置。[KFP go.mod L43](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/go.mod#L43)，[Viper v1.20.1 L1144–1234](https://github.com/spf13/viper/blob/v1.20.1/viper.go#L1144-L1234)

| 键 | getter 在未设置时的默认值 |
|---|---|
| MULTIUSER | false |
| MULTIUSER_SHARED_READ | false |
| REQUIRE_NAMESPACE_FOR_PIPELINES | false |
| TOKEN_REVIEW_AUDIENCE | pipelines.kubeflow.org |
| POD_NAMESPACE | kubeflow |
| KUBEFLOW_USERID_HEADER | x-goog-authenticated-user-email |
| KUBEFLOW_USERID_PREFIX | accounts.google.com: |

依据：[config.go L53–68, L111–156](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/common/config.go)，[const.go L48–54, L75–77](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/common/const.go)。这些均不是现场有效值。

`GET <base>/apis/v2beta1/healthz` 设置 application/json 并输出 commit_sha、tag_name、multi_user、pipeline_store。v1beta1 同名路径少 pipeline_store；handler 内无业务资源认证。匿名健康接口成功不说明资源认证被绕过。[main.go L374–384](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/main.go#L374-L384)

官方镜像 Dockerfile COPY 配置目录到 `/config`、WORKDIR 为 `/bin`，默认 CMD 为：

```text
/bin/sh -c /bin/apiserver --config=/config --sampleconfig=/config/sample_config.json -logtostderr=true --logLevel=${LOG_LEVEL}
```

因此 `/config/config.json` 是该 Dockerfile 对应镜像的定点核对位置。当前 Pod 若无 command/args，仍要匹配实际镜像来源或只读核对进程 argv；Dockerfile 不能证明某个镜像 digest 的实际内容。[Dockerfile L70–89](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/Dockerfile#L70-L89)

## K04：Bearer / 用户头、TokenReview 和最终 principal

认证器顺序是 HTTP 用户头，然后 Bearer TokenReview；ResourceManager 遇到首个无 error 的认证结果便停止尝试。用户头匹配成功时，源码不会再要求 Bearer token 成功。[auth.go L35–44](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/auth/auth.go#L35-L44)，[resource_manager.go L1930–1943](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/resource/resource_manager.go#L1930-L1943)

`KUBEFLOW_USERID_HEADER=':'` 没有特殊 disable 分支：它成为字面 metadata 键 `:`。HTTPHeaderAuthenticator 原样使用键/prefix；辅助函数将键转小写，要求恰好一个值并匹配 prefix，缺失便失败。正常 HTTP 请求预期找不到该键而回落 TokenReview，是对实际传输行为的推断；单凭此配置不能证明所有可达入口都强制 TokenReview。[authenticator_http_headers.go L26–35](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/auth/authenticator_http_headers.go#L26-L35)，[auth/util.go L30–55](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/auth/util.go#L30-L55)

TokenReview 请求包含配置的 audiences；响应必须 authenticated=true 且包含全部要求的 audience，最后只返回 `status.user.username`。如执行与此版本一致的人工 review，应发 `POST /apis/authentication.k8s.io/v1/tokenreviews`，在进程内填 token，并显式填现场确认的 audience。review 与真实 KFP GET 是不同证据。[authenticator_token_review.go L47–108](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/auth/authenticator_token_review.go#L47-L108)

`MULTIUSER=false` 先跳过授权；`MULTIUSER_SHARED_READ=true` 对 get/list 在取得身份前提前放行。SAR 只传 User 与 ResourceAttributes，不传 Groups / UID / Extra。[resource_manager.go L1911–1955](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/resource/resource_manager.go#L1911-L1955)

## K05 / K06：namespace 链与准确 review 属性

Run 创建先调用 GetValidExperimentNamespacePair：已有 Experiment 决定 namespace；同时显式声明不同 namespace 会报错。创建 Workflow 时将 manifest namespace 覆写为 Run namespace，空值才回落 POD_NAMESPACE。返回后记录真实 Workflow 名到 Run.K8SName；不能把 API 的 display_name 或任意 Pod label 当该关联。[run_server.go L137–159](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/server/run_server.go#L137-L159)，[resource_manager.go L578–609, L2093–2105](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/resource/resource_manager.go#L578-L609)。[官方 multi-user 文档](https://www.kubeflow.org/docs/components/pipelines/operator-guides/multi-user/)给出 Experiment/Run namespace 关系，但该页也包含旧版“pipeline 全局共享”的历史说明；2.16.0 的 Pipeline namespace 行为应以固定源码与现场配置判断。

下表只在已确认进入对应 SAR 分支时有效。统一 `POST /apis/authorization.k8s.io/v1/subjectaccessreviews`；spec.user 必须为 KFP 实际认证后 principal，不能用管理员自身替代，也不能额外加入 groups 改变语义。

| resourceAttributes 字段 | 精确 PipelineVersion GET | Run create 权限询问 |
|---|---|---|
| namespace | 真正父 Pipeline.namespace | 已解析的 Run / Experiment namespace |
| group | pipelines.kubeflow.org | pipelines.kubeflow.org |
| version | v1beta1 | v1beta1 |
| resource | pipelines | runs |
| verb | get | create |
| name | 真正父 Pipeline.name | 省略或空 |
| subresource | 省略 | 省略 |

GetPipelineVersion 根据存储 Version 的父 PipelineID 取父 namespace/name；共享空 namespace 直接跳过认证，`IsEmptyNamespace` 同时接受空串和 `-`。[pipeline_server.go L1061–1106](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/server/pipeline_server.go#L1061-L1106)，[resource_manager.go L2050–2055](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/resource/resource_manager.go#L2050-L2055)

Pipeline GET 的 `.name` 明确映射内部 Pipeline.Name，因此该版本可以用返回 `.name` 构造精确 SAR，不能换成 `.display_name` 或 UUID。Version GET handler 使用 version_id 取对象，不能只凭 URL 中 parent ID 推定关系；必须核对返回 `.pipeline_id` 与 `.pipeline_version_id`，同时检查业务 error。[api_converter.go L313–319, L479–486](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/server/api_converter.go#L313-L319)，[pipeline_server.go L853–862](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/server/pipeline_server.go#L853-L862)，[固定 v2beta1 Pipeline Proto](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/api/v2beta1/pipeline.proto#L117-L120)

Run create 不传 runId 给 canAccessRun，因此不会填 name；已有 Run GET 的授权则可能填 Run.K8SName。SAR version 固定 v1beta1，不因 REST v2beta1 改变。[run_server.go L137–159, L638–668](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/server/run_server.go#L638-L668)，[const.go L18–38](https://github.com/kubeflow/pipelines/blob/e4ebca310f404dac306e16bc880de12bbbf63b95/backend/src/apiserver/common/const.go#L18-L38)

## K07：watch 默认行为

- Argo 3.7.3：`--namespaced=false`、`--managed-namespace=''`；namespaced=false 会忽略非空 managed namespace，true 且 managed 为空才用安装 namespace。支持 ARGO_ flag env 映射，故还要核对 ARGO_NAMESPACED / ARGO_MANAGED_NAMESPACE。workflow informer 使用 managed namespace，并有 ConfigMap InstanceID 的 selector；watch 范围不能仅从 WATCH_NAMESPACE 是否存在推断。[main.go L97–113, L193–213](https://github.com/argoproj/argo-workflows/blob/bded09fe4abd37cb98d7fc81b4c14a6f5034e9ab/cmd/workflow-controller/main.go#L97-L113)，[controller.go L305, L439–445](https://github.com/argoproj/argo-workflows/blob/bded09fe4abd37cb98d7fc81b4c14a6f5034e9ab/workflow/controller/controller.go#L305)
- Trainer 2.1.0：entrypoint/config types 没有 namespace watch flag，也未设置 cache DefaultNamespaces / ByObject namespace 限制；锁定 controller-runtime v0.22.3。其默认 cache 在 DefaultNamespaces 为空时使用 NamespaceAll。这是源码默认值，现场仍需匹配实际二进制、args/config 与 RBAC。[Trainer main.go](https://github.com/kubeflow/trainer/blob/73c9bece741ce17d2124abda3ec6bfcf5b5b8e87/cmd/trainer-controller-manager/main.go)，[config loader](https://github.com/kubeflow/trainer/blob/73c9bece741ce17d2124abda3ec6bfcf5b5b8e87/pkg/config/config.go)，[go.mod L22](https://github.com/kubeflow/trainer/blob/73c9bece741ce17d2124abda3ec6bfcf5b5b8e87/go.mod#L22)，[cache.go L360–366](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.22.3/pkg/cache/cache.go#L360-L366)
- JobSet 0.10.1：同样未发现 namespace watch flag/config 与 DefaultNamespaces 限制；锁定 controller-runtime v0.22.1，其相同默认分支使用 NamespaceAll。[JobSet main.go](https://github.com/kubernetes-sigs/jobset/blob/da72d32bb0060354aa04b1a5868f3b363b088446/main.go)，[config loader](https://github.com/kubernetes-sigs/jobset/blob/da72d32bb0060354aa04b1a5868f3b363b088446/pkg/config/config.go)，[go.mod L23](https://github.com/kubernetes-sigs/jobset/blob/da72d32bb0060354aa04b1a5868f3b363b088446/go.mod#L23)，[cache.go L360–366](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.22.1/pkg/cache/cache.go#L360-L366)

上述默认 watch 解释不证明目标资源 RBAC，也不证明对任一现有对象完成 reconcile。WATCH_SCOPE、RBAC、OBSERVED_RECONCILIATION 应分别取证。

## 追加：现场 healthz ref 与 tag ref 不同（仅相关文件核对）

主代理提供的 READ_API healthz 为 `commit_sha=85a38e0719870e5b2f97a74f5c966d79bcc7c960`、tag_name=2.16.0、multi_user=true、pipeline_store=database。该值与本文解析的 tag commit `e4ebca310f404dac306e16bc880de12bbbf63b95` **不相等**，保留 ref 差异。

已向[官方 commit API](https://api.github.com/repos/kubeflow/pipelines/commits/85a38e0719870e5b2f97a74f5c966d79bcc7c960) 发 GET，HTTP 200；[此 commit 确实存在](https://github.com/kubeflow/pipelines/commit/85a38e0719870e5b2f97a74f5c966d79bcc7c960)。随后仅取这七个相关源文件并与本文已取 tag 源文件 SHA256 对比，全部 byte-identical：main.go、common/config.go、auth/auth.go、auth/authenticator_token_review.go、resource/resource_manager.go、server/pipeline_server.go、server/run_server.go。精确 URL、UTC、哈希与逐项对比保存在 归档内 [AN-controller-matrix.json](AN-controller-matrix.json) 的 upstream_source_provenance.reported_runtime_commit_comparison。核对覆盖的 healthz、配置读取、认证器顺序、audience 检查、SAR 构造及已查 namespace 授权行为一致；没有声称整个仓库、依赖、镜像或二进制等价。

主代理另报告原 Pod argv 含 `--config=/config`，并定点读取 `/config/config.json` 的七个目标键均缺失；这些是主代理采集的配置证据，本研究未自行进入 Pod。结合显式 env 与上述配置优先级，可以按现场 env 解释相应键，不能将它们标成默认值。healthz commit 字段本身从配置读取，仍不能替代镜像构建来源证明。
