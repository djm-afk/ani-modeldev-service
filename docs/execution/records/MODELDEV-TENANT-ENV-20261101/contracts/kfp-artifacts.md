# KFP artifact 配置与固定代码依据

本文件是spec第10节的补充；并非真实I/O已通过的证明。

## 固定路线

使用KFP 2.16现场health SHA 85a38e0719870e5b2f97a74f5c966d79bcc7c960支持的S3 SecretRef。每个接入Namespace固定：
- kfp-launcher ConfigMap：providers/defaultPipelineRoot；
- modeldev-kfp-artifacts Opaque Secret：access_key/secret_key；
- metadata-grpc-configmap：METADATA_GRPC_SERVICE_HOST/PORT；
- ani-kfp-ca：实际CABUNDLE_KEY_NAME对应的公开CA key；
- modeldev-step-owner：config.json、modeldev-ca.pem、s3-ca.pem；
- modeldev-execution-environment：binding.json/binding.sha256（无秘密）。

providers模板、不可变与轮换规则以spec为准。KFP artifact AK仅限专用内部bucket；共享AK不是tenant IAM隔离。业务STS/input/base/publish继续原精确scope。

## 源码所确认的调用关系

| 源文件（下列URL使用同一SHA） | 确认内容 |
|---|---|
| backend/src/v2/driver/root_dag.go，80–112 | 从Run root和运行namespace的launcher配置生成store session信息并交给metadata |
| backend/src/v2/config/env.go，72–82 | 读取运行namespace内名为kfp-launcher的CM |
| backend/src/v2/config/s3.go，25–69 | s3.default字段为endpoint/region/disableSSL/forcePathStyle/maxRetries/credentials；SecretRef显式映射三个名字 |
| backend/src/v2/component/launcher_v2.go，215–242 | 业务组件执行前已尝试打开artifact bucket |
| backend/src/v2/objectstore/object_store.go，305–334 | 通过Kubernetes GET同namespace Secret，使用AK/SK，session token为空 |
| backend/src/v2/component/constants.go，25–27 | metadata环境键为METADATA_GRPC_SERVICE_HOST/PORT |
| backend/src/v2/compiler/argocompiler/common.go，29–37 | 编译器引用metadata CM与CA配置 |
| backend/src/v2/component/launcher_v2.go，568–582 | 部分CA环境处理发生较晚，不能以存在CA挂载证明早期OpenBucket已信任证书 |

## 公开部署输入

只补取实际artifact HTTPS endpoint/region/bucket、专用凭据文件引用、公开CA与CABUNDLE_KEY_NAME、metadata host/port、driver/launcher TLS flags。不得输出AK/SK，不能把[VALUE_NOT_COLLECTED]当实际值，不能凭providers不存在的caFile字段修TLS。

真实验证必须观察固定launcher对内部artifact的读写与正常MLMD通信；metadata CM本身不是TLS开关。禁止HTTP、skipTLS、业务管理员凭据和匿名fallback。

## 源码链接

- [root_dag.go](https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/v2/driver/root_dag.go)
- [config/env.go](https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/v2/config/env.go)
- [config/s3.go](https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/v2/config/s3.go)
- [launcher_v2.go](https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/v2/component/launcher_v2.go)
- [object_store.go](https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/v2/objectstore/object_store.go)
- [constants.go](https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/v2/component/constants.go)
- [argocompiler/common.go](https://github.com/kubeflow/pipelines/blob/85a38e0719870e5b2f97a74f5c966d79bcc7c960/backend/src/v2/compiler/argocompiler/common.go)
