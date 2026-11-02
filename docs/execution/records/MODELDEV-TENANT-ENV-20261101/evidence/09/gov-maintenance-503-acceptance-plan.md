# 旧Gov维护候选的后续503停入验收输入（尚未执行）

当前只读预检：`gov-maintenance-predeploy-20261031T212719Z.json`。部署读取UTC21:27:19–21:27:22；必要SQL补读UTC21:28:26–21:28:28，完整集群身份均匹配、SSH exit0。只有精确 `ANI_MODELDEV_CREATE_PAUSED` 的存在性/值被处理和记录，没有保存其他env值。

- Deployment：ani-system/ani-governance，UID `4ae87828-5f2c-4b27-a8b8-d348aeb86ee5`、RV20366558、generation197；container=governance；replicas/ready=1/1，RollingUpdate。当前image/imageID均为 `10.10.1.66:30003/ani-gpu-platform/ani-governance@sha256:0518f2d3616c8a088113b0e604540565d4419ae1921446d17ca4b233c4090fc0`。
- 原Pod：ani-governance-78b5778b75-nc5gg，UID `76211ecd-65c5-43f2-9c17-f373df78ce20`，startTime=2026-10-31T11:24:12Z；ReplicaSet→Deployment原UID链已核对。
- Deployment/Pod均没有显式pause env。唯一envFrom为无prefix Secret ani-governance-environment，UID `59266531-0691-4e42-b7aa-dfc972716e9d`、RV14606584；精确查其目标key不存在。因此当前Kubernetes配置没有false/非法值覆盖候选默认行为。没有读取现存Pod进程env；此观察不是当前旧镜像已暂停的证明。新Pod会使用替换时的配置，写前仍须重核。
- DB显式BEGIN READ ONLY，实读transaction_read_only=on：active Create lease=0，任何未到期Create lease=0；原4UNKNOWN（55/57/59/60）仍retry_blocked=true、last_error_code=INVALID_COMMAND、lease_until=null。首SQL因将原varchar operation_id与uuid常量比较而失败，纠正为原text精确比较后成功；没有修改DB。

## 精确HTTP入口

通过唯一SSH入口在node1访问 Service：`http://10.96.44.233:7788`，集群DNS为 `http://ani-governance.ani-system.svc:7788`；Service UID `2d4310e8-e5ed-4aef-b8d1-2af22dbd83c8`，NodePort30085。匿名GET `/healthz`实得404（19bytes），仅证明HTTP可达，不宣称健康检查或暂停通过。

## 本地旧Gov源码合同

根目录 `/home/chabking/workspace/.worktrees/governance-modeldev-create-maintenance`，读取时base HEAD `6a7c721ea113d51497421d7975122ec7ca8105c2`；default-pause文件是本轮候选进行中的工作树变更，不能当作该base提交已经发布。精确文件SHA已收入同一JSON回执，最终候选SHA/门禁仍由resource和root交付。

- 租户登录：`POST /api/v1/auth/password/login`，Content-Type application/json，字段tenant_name、username、password；tenant_name是sys_tenants.code。本轮没有登录。后续按用户既有授权使用tenant-a/admin，口令、Cookie和access_token只保留进程内，不落证据。成功token字段为access_token；平台root登录不符合Create要求的tenant-user身份。
- 原生产来源规则：携带Origin/Referer时使用console来源白名单；原非浏览器合同允许两者均缺省（auth_origin.go:93–119），不用为此读取或保存其他env白名单值。
- Create：`POST /api/v1/modeldev/executions`，精确一个 `Authorization: Bearer <tenant-user access_token>`，不混用签名头；Content-Type application/json，无query。先通过原认证和route授权，strict body≤16384bytes，不接受重复字段/null/未知字段。
- 最小解析输入字段：name、kind、implementation_id、compute_preset_id、dataset_version_id、parameters、idempotency_key。源码parser fixture使用kind=GENERAL_TRAINING、implementation_id=builtin.mlp、parameters={}和合法UUID格式的compute/dataset字段；同一JSON回执有完整**仅解析层**示例，未选择真实资产或执行Create。
- 新维护候选启动规则（modeldev_maintenance_config.go）：缺省/空/true暂停，精确false恢复，其他值启动失败。ModelDevService.CreateExecution先验证tenant-user，维护gate拒绝在resolver/acceptance/charge前返回HTTP503，reason必须精确 `MODELDEV_CREATE_MAINTENANCE`（modeldev_service.go:93–118）。认证/授权失败或无效JSON不会证明维护gate生效。
- 同一个gate装配给原publisher；paused begin不得进入ClaimDelivery。Stop/close/release不走此gate。当前lease=0不是部署后无新claim的证据。

## root后续操作的验收边界

root取得通过必要门禁的新旧API维护候选SHA后，按原Workbench合同只更新此Deployment的governance image。实际写前重核完整identity、原Deployment UID/RV/镜像及pause key来源，使用Workbench的run_verify=true；本项没有执行部署。

发布后核对新Pod UID/startTime/imageID与新候选digest，再按上述原生产认证合同执行一次有合法解析输入的Create，断言HTTP503及精确reason；登录/请求/凭据不混入部署前证据。随后只读核验没有新的acceptance/charge或publisher claim/attempt，等待旧活跃lease退出。需要实证Stop/close/release保持可用，不能用readiness、404 health probe、源码测试或本次零lease观察替代。

本项只完成部署前输入，不声明已暂停/已排空/已切v4，未改变3条原执行的关闭资格。
