# 07 Gov 当前租户入口、授权、错误与预算

Status: ready-for-agent
Resolution: resolved
Blocked by: None

## Goal

提供当前租户显式环境Ensure/Get的BFF入口，并确保环境错误在原Create占额前被准确返回。
依据：[spec.md](../spec.md) §2.3、§4、§7.4、§12.5、§14.2；只消费正式ModelDev API，不新增租户初始化或quota业务。

## Scope

- 仅 ani-governance；api/protos/admin/service/v1/i_modeldev.proto、新api/protos/modeldev/service/v1/environment.proto。
- app/admin/service/internal/server/modeldev_environment_http.go、instance_timeout.go、rest_server.go。
- app/admin/service/internal/service/modeldev_environment_service.go、modeldev_service.go。
- app/admin/service/internal/data/modeldev_environment_client.go、modeldev_authorization.go、modeldev_client.go及必要wiring。
- 正式Go module pin、固定API/OpenAPI生成物、权限迁移；docs/contracts/frontend-http-api.md、service-to-service-calls.md。

## Implementation

1. 新增POST /api/v1/modeldev/environment:ensure与GET /api/v1/modeldev/environment，路径与DTO严格按01合同。
2. Ensure只收≤1024字节application/json空对象加EOF，禁止query，恰一个canonical非零UUID Idempotency-Key；Get禁止body/query。
3. 复用Principal且SubjectUser、tenantID>0，重新检查当前用户/租户/角色/ALL范围、动作Permission及精确API grant。
4. 登记manage_environment/view_environment Permission/API关联与默认租户管理员授权；只读角色须明确获授，不依赖菜单。
5. ResourceTenantID取当前权威映射；丢弃浏览器x-ani-*，重建tenant/actor/独立requestID/精确authorized-method/tenant-all。
6. 通过正式module pin调用两个RPC；严格拒绝request/response/nested view/ErrorDetail/FieldViolation未知字段及非法READY。
7. Ensure RPC≤28秒、HTTP≤30秒，Get RPC≤3秒、HTTP≤5秒；精确路径更新timeout filter，服从上游剩余预算。
8. PROVISIONING映射202和Retry-After=2，其他持久state200；Busy按实际state，Get无登记404，不伪造开通成功。
9. 不缓存首次PROVISIONING、不循环sleep等READY；同key重试重新授权并返回最新持久结果，operation_id只作关联。
10. 为Prepare/Finalize新增严格环境错误白名单：Unavailable/ENVIRONMENT_NOT_READY/tenant_environment/一致correlation/单environment violation。
11. 仅HTTP metadata.environment_reason输出有限枚举；Get NotFound+38独立严格转换，不能被通用query错误吞掉。
12. 保留当前ComputeSelection、AcceptFrozen与GPU原charge同事务及可靠CREATE投递；环境接口不调用该链，环境失败在其前终止。

## Acceptance

- API01：当前tenant权限、tenant0拒绝、撤权后的同key重试拒绝、伪造metadata/任意body-query-header和未知proto拒绝。
- API01：精确两路径实际预算生效；其他ModelDev路由预算未整体放宽，无请求内sleep/轮询或首响应缓存。
- API02：所有允许environment_reason准确通过，其余kind/violations/上游message被拒；ErrorReason38保留为404。
- API02：Ensure/Get、缺环境及不READY的Prepare均无acceptance/charge/可靠CREATE副作用。
- ST02：无环境合法tenant的source-only Readiness保持；CPU不被要求Gov推导cluster，GPU错cluster不接受。
- 正式API pin、生成物、Permission/API关联、OpenAPI和文档一致；真实A/B授权、查询/Stop隔离留LIVE03/LIVE08。

## Constraints

- 不新增任意target tenant、namespace/cluster/Experiment输入，不加平台tenant0代办接口。
- 不实现完整tenant初始化、套餐/菜单/新页面、租户namespace字段或新的quota/outbox。
- 不允许业务提交隐式Ensure，不把queryContext复用误当Ensure免授权，不用硬编码管理员ID。
- 不透传任意上游错误文本/metadata，不放宽全ModelDev前缀timeout，不将unknown fields静默丢弃。
- 不提交本地replace或复制ModelDev生成代码；线上路由切换和旧Create停入操作由09统一执行。

## Result

### 本轮最终回填（2026-11-02 Asia/Shanghai）

- 当前结果：**PASS / RESOLVED**。正常租户Ensure/Submit/Get/logs/download/Stop与双向跨租户拒绝已有真实证据；本轮A4/B5及正常A→B404再次成功，见 evidence/10/completion-multitenant-regression/result.json。
- 组件、外部double与真实现场范围分开；按用户要求本轮未重跑全量make verify，未实测GPU_WHOLE。具体证据及边界见[完整报告](../implementation-report.md)与[逐项验收](../acceptance-results.tsv)。

### 历史固定源码与此前checkpoint（保留原事实，当前状态以上文为准）

- Outcome: PASS（本项源码/组件/必需gate）；Resolution: resolved。显式Ensure/Get及Prepare前置失败已在真实JWT/Redis/Casbin/PG授权与生产HTTP/service/client中验证；只有外部ModelDev mTLS及Resource服务边界为明确替身。指定集群A/B业务授权与实际平台连接仍由09/10负责，未记LIVE通过。
- Base / final commits: Gov base `ddd98f4c79751ba848fa862bab6878dc7bc99bb7`；原运行/API提交 `fa80f886d022177173d7e9cf81ec51e54c2df4c8`、HTTP补测 `5cfa9065190b9585b799e428537a0019ca70e41a`、非秘密fixture分组说明 `f6a68ed7f89cca56b4ea97377a21e4146f4c6b62`、最终既有VM权限map同步 `02f4f71e743b4ffe08ef04e880f1c431b62d7213` 均已正常SSH推送。最终部署候选使用02f4，不把fa80旧快照当最终源树。原44文件manifest、两个独立补测hash和最终四文件hash按提交串联，见evidence/gov-final/fedora-round2-source-identity.json、environment-http-integration-source-and-coverage.json、gitleaks-fixture-grouping-source.json、vm-log-map-final-proof.json。
- Changed files / generated artifacts: 官方API/OpenAPI、当前tenant显式Ensure/Get、精确权限/metadata/预算/Error38有限转换、Permission迁移/权威map及Create/publisher维护gate。正式ModelDev module `v0.0.0-20261031195825-d807cbdbd5dd` 和Resource `v0.0.0-20261031180451-71b3ad7a2a88`；没有replace或复制pb。最后四文件仅将已存在SQL的VM日志GET→vm:read补入capability TSV、固定生成capability_gen.go及维护两处覆盖计数测试；未改SQL/role/plan grants/VM runtime/Inference/扫描规则。
- Validation commands / exit codes / required gates: 原default/race session44762 exit0（data15.993s/service1.453s/server1.941s），真实隔离PG六项exit0/0.294s；相关完整日志均已取回，独占临时PG已清理。HTTP定向race exit0/1.960s（environment-http-integration-final.log）。最后固定生成及--check exit0；permcode/bootstrap定向race exit0（1.020s/5.905s）；最终 `make check` 于UTC2026-10-31T21:07:47Z–21:13:38Z真实exit0，包含checks.sh末段完整audit，同四文件hash快照：实际调用漏洞0、另有未调用导入包1/required module10观察保留、Gitleaks35,310,357bytes无泄漏。证据evidence/gov-final/vm-log-map-{make-check.log,make-check-receipt.json,final-proof.json}。这不是把先前子项拼接为完整gate；没有在无变更后再重跑audit。
- Acceptance IDs / actual values / evidence paths: API01覆盖独立manage/view、精确API/ALL范围、tenant0/API-key拒绝、真实PG撤权后重验；实际JWT/Redis/Casbin/PG/生产HTTP→mTLS peer实跑Ensure202→200、Get200/无行404，同JWT同key撤manage后403且全部4类上游RPC计数不增、view独立。严格body/query/header/未知proto和28/3秒RPC、30/5秒HTTP父预算默认组PASS。API02真实HTTP Prepare503仅environment_reason=TENANT_NAMESPACE_NOT_FOUND，每请求acceptance/charge0；全部22cause由converter单测覆盖，HTTP只实跑1cause。证据environment-http-integration-{final.log,final-receipt.json,source-and-coverage.json}及environment-formal-{default-tests,pg-tests}.log。权限迁移manager/template各2grant、viewer0且重入保持viewer0；不声明已实测既有OFF grant重入。原CPU无GPU账、原GPU可靠受理及同key一次占额由真实PG六项验证。NS01只消费正式v4 codec，不宣称Gov拥有Namespace resolver或复制完整vectors。
- Deployment or cleanup effects / preserved assets: 未更新线上DB/集群或服务，未应用权限SQL或角色/套餐授予。旧API维护候选 `6a7c721ea113d51497421d7975122ec7ca8105c2` 独立保留且未部署，其旧gate失败没有被本次final结果覆盖；final v4 decoder未用于旧v3排空。原Stop/close/release协议不变。
- Remaining blockers / affected dependencies: 本项相关必需gate已解除；没有运行中的远端检查或以SSH超时残留的RESULT_UNKNOWN。09原三执行关闭证据阻断v4 rollout和真实A/B验收，现场授权/Stop/下载矩阵NOT_RUN，不能由本项组件PASS替代。
- Contract conflicts / evidence / minimal correction: 原make check因缺既有VM日志map失败，先前audit网络恢复后因原负向fixture命中失败，原输出完整保留。最小修正一：在12个原非秘密负向值之间加准确分组注释，所有值/顺序/断言逐项相同，不加allowlist，定向及完整audit均exit0（gitleaks-fixture-grouping-source.json）。最小修正二：原base SQL已明确GET日志→vm:read；原base与当前OpenAPI均28个VM路由且集合相同，27路来自原register-vm-permissions.sql、1路来自原register-security-group-permissions.sql，两SQL哈希与base完全相同，故补1行map、派生总量各+1，并把过时VM26覆盖计数维护为有来源证明的28，未删除断言或新增权限。首次targeted的实际FAIL保留，修复后完整make check已真实通过；证据vm-log-map-existing-28-contracts.json、vm-log-map-targeted.log和最终proof。

最新测试fixture补充：244780ffe6b9a7deb1c3f6f162e474e76a73a566仅固定原timeutil测试的UTC时区，原生产diff.go SHA/断言不变；与旧API维护32c61相同修正，三时区race包测试均exit0。原02f4完整check/audit PASS仍准确对应旧固定源码，新候选完整Workbench gate待实际结果，不拼接冒称。见evidence/gov-maintenance/maintenance-timezone-commits.json。

## Comments

- 初始任务，尚无实施或验收完成声明；后续按时间追加事实与证据。
- 达到本项验收后再更新 Resolution 和 Result；后续现场证据不得提前标记通过。
