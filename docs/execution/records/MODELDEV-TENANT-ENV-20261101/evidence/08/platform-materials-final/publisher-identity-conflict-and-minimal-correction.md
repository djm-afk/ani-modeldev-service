# 临时平台发布身份：待批准的最小修正

状态：仅准备，未创建 SA/Role/RoleBinding/Job，未签发 token，未发布定义。

受影响条款是 spec.md §6.2「发布器使用已有受管部署身份」及 contracts/kfp-rbac.md 的部署身份边界。现场原四个 upload.py 实际均为 `modeldev-control` 临时 TokenRequest（原脚本行7/14/17），没有独立 publisher；当前 ani-system 授权没有可复用的明确发布主体。详见 original-publication-source.json、existing-publisher-identities.json。runtime control 的 create/list 拒绝符合本规格，不是需要加权的故障。

提议仅新增 ani-system 内 `modeldev-material-publisher-d807` 的 SA、同名 Role、RoleBinding。精确三对象在 proposed-publisher-rbac.json；runtime control 不变，无 ClusterRole/ClusterRoleBinding，无 runs、experiments、Secret、TokenRequest 或 Kubernetes 对象写权限。

固定 KFP health SHA 的真实 SAR 见 publisher-sar-contract.json 和 pinned-kfp-source.json。重点：JSON 创建父 Pipeline 带固定 name；但本仓库原 publisher 使用的 multipart upload_version 对应 pipelines/create **name 为空**，不是 update。两种 List 的 name 也为空。故本上传路径只能把 create/list 限定到 ani-system，不能诚实声称 RBAC 只允许创建四个名字；get 则按四个固定父名 resourceNames 限定。发布器源和不可变 IR 另外强制只有四个固定父/摘要 Version。要进一步收窄 create 到 name，需改变上传协议/源码，不属于本最小修正。

批准后才执行的最小窗口：创建 suspend=true 的单次 Job 取得真实 Job UID；三个 RBAC 对象附此 Job 原 UID 的同 Namespace ownerReference 后创建，完成真实 SAR/原投影 token 认证检查再启动。Job 使用最终 CPU 镜像 digest、只读 publisher/IR/公开 CA 输入；独立 serviceAccountToken 投影 audience=pipelines.kubeflow.org，expirationSeconds=900，automount=false；不由操作员调用 TokenRequest、不把 token 带回本地。Job activeDeadlineSeconds=1200、backoffLimit=0、ttlSecondsAfterFinished=300，父 Job 删除会 GC 所属三 RBAC 对象。发布前每次写仍要求 sole SSH 刷新的完整 identity receipt ≤60s，失效即停止，不自延长/换身份。

成功或失败都先保留原子 publication journal 与精确 GET 结果。操作员 finally 按实际原 UID 先撤 RoleBinding，再停止/删除本 Job、证明 Pod/原 token 来源消失，删除原 SA/Role 和临时输入；Job TTL+ownerReference 为断线兜底。RBAC 本身没有 TTL，不把 token 到期或 Job 完成当作已经撤权的证据。未知 POST 只观察，绝不第二次盲发。最终复核三 RBAC 对象/Job/Pod 不存在、秘密临时文件已清，保留不可变定义和 journal。

仍需用户明确批准这一项身份合同修正；本回执不授予执行权限。
