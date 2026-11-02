# 环境 API 固定合同

主规格：spec.md第4–5节。此文件仅固定wire形状；不得改名、增tenant/namespace入参或复用执行Create接口。

## HTTP

POST /api/v1/modeldev/environment:ensure，Content-Type application/json，body恰{}，上限1024字节，无query；恰一个Idempotency-Key（规范非零UUID）。
GET /api/v1/modeldev/environment，无body/query。

响应采用snake_case和environment外层对象。只有持久PROVISIONING为202；其余存在的state为200；无行Get为404。错误使用既有Gov结构，按spec有限映射。

## ModelDev proto source 形状

下列片段加入api/ani/modeldev/v1/tenant_environment.proto，使用本模块现有package/go_package和google.protobuf.Timestamp导入。通过固定生成流程，不把此示意片段作为手写pb.go。

~~~proto
enum TenantExecutionEnvironmentState {
  TENANT_EXECUTION_ENVIRONMENT_STATE_UNSPECIFIED = 0;
  TENANT_EXECUTION_ENVIRONMENT_STATE_PROVISIONING = 1;
  TENANT_EXECUTION_ENVIRONMENT_STATE_READY = 2;
  TENANT_EXECUTION_ENVIRONMENT_STATE_BLOCKED = 3;
  TENANT_EXECUTION_ENVIRONMENT_STATE_NEEDS_REVIEW = 4;
}
enum TenantExecutionEnvironmentPhase {
  TENANT_EXECUTION_ENVIRONMENT_PHASE_UNSPECIFIED = 0;
  TENANT_EXECUTION_ENVIRONMENT_PHASE_NAMESPACE = 1;
  TENANT_EXECUTION_ENVIRONMENT_PHASE_RESOURCES = 2;
  TENANT_EXECUTION_ENVIRONMENT_PHASE_EXPERIMENT = 3;
  TENANT_EXECUTION_ENVIRONMENT_PHASE_VERIFY = 4;
  TENANT_EXECUTION_ENVIRONMENT_PHASE_PUBLISH = 5;
  TENANT_EXECUTION_ENVIRONMENT_PHASE_COMPLETE = 6;
}
message TenantExecutionEnvironmentView {
  string environment_id = 1;
  string cluster_id = 2;
  string namespace_name = 3;
  optional string namespace_uid = 4;
  TenantExecutionEnvironmentState state = 5;
  TenantExecutionEnvironmentPhase phase = 6;
  string reason_code = 7;
  bool retryable = 8;
  uint32 retry_after_seconds = 9;
  optional string binding_digest = 10;
  optional string experiment_id = 11;
  google.protobuf.Timestamp updated_at = 12;
  google.protobuf.Timestamp last_verified_at = 13;
}
message EnsureTenantExecutionEnvironmentRequest {
  string operation_id = 1;
}
message GetTenantExecutionEnvironmentRequest {}
message EnsureTenantExecutionEnvironmentResponse {
  TenantExecutionEnvironmentView environment = 1;
}
message GetTenantExecutionEnvironmentResponse {
  TenantExecutionEnvironmentView environment = 1;
}
service ModelDevTenantEnvironmentService {
  rpc EnsureTenantExecutionEnvironment(EnsureTenantExecutionEnvironmentRequest)
      returns (EnsureTenantExecutionEnvironmentResponse);
  rpc GetTenantExecutionEnvironment(GetTenantExecutionEnvironmentRequest)
      returns (GetTenantExecutionEnvironmentResponse);
}
~~~

所有unknown fields拒绝，UNSPECIFIED不能成功回包。Gov DTO将枚举映射为短字符串PROVISIONING/READY/BLOCKED/NEEDS_REVIEW及NAMESPACE/RESOURCES/EXPERIMENT/VERIFY/PUBLISH/COMPLETE；不直接把长proto枚举名暴露为另一套前端协议。

## 字段与状态约束

- environment_id、cluster_id、tenant UUID均规范非零UUID；namespace_name来自可信tenant算法。
- 未获得namespace_uid/experiment_id/binding_digest时省略，不输出null或占位UUID。
- updated_at始终存在；last_verified_at尚无完整检查时省略；均UTC RFC3339。
- READY必须phase=COMPLETE，包含原namespace_uid、experiment_id、binding_digest和last_verified_at；reason_code为空，retryable=false，retry_after_seconds=0。
- PROVISIONING必须phase在NAMESPACE..PUBLISH；retryable=true，retry_after_seconds=2；正常推进时reason_code为空，KFP结果不确定时使用其有限原因；ENVIRONMENT_PROVISIONING仅用于业务受理拒绝。锁忙不改当前持久原因。
- BLOCKED/NEEDS_REVIEW保留失败phase及已确认材料；NEEDS_REVIEW不可自动换UID或重建对象。
- Ensure锁忙返回原持久state，不写一个伪PROVISIONING。
- 响应不含tenant用户列表、token、Secret值、kubeconfig、bucket凭据或DB连接。
- Get只读，Ensure有限推进；无自动后台开通。

ErrorReason新增38=ERROR_REASON_ENVIRONMENT_NOT_REGISTERED；保留现有reserved字段。详细有限cause与Gov白名单见spec第4.4节。
