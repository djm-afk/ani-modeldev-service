# 指定更新入口之外的完整交付清单

记录时间：2026-11-01。此清单用于回答用户的范围与大小问题，不表示获准绕过指定更新入口，也不表示已经部署。

## 镜像

大小采用实际构建收据的 Podman `Size` 字节数，即本地镜像大小；不是压缩后 registry 传输量。尚未构建的最终镜像不借用旧版本大小。

| 镜像用途 | 数量 | 实际大小 | 当前状态 | 指定入口覆盖 |
|---|---:|---:|---|---|
| ModelDev 主服务 v4 | 1 | UNKNOWN，最终镜像未构建 | 源码及必要测试完成；未部署 | 已有 modeldev 项目，仅更新主容器镜像 |
| Governance 最终动态环境版 | 1 | UNKNOWN，最终镜像未构建 | 源码完成；未部署 | 已有 governance 项目 |
| Resource 最终版 | 1 | UNKNOWN，最终镜像未构建 | 源码及 UID 窗口工具完成；未部署 | 已有 resource 项目 |
| Governance 旧 API 临时停入维护版 | 1，切换中间态 | UNKNOWN，当前任务仍 verify | job 8d8b448e1a8846c0ba96bb391d323a7e，固定 commit 32c61fcbd57c70ca996d62ad2b234ba84ff0b052 | 已通过入口提交，run_verify=true |
| ModelDev step | 1 | 36,725,738 B = 35.02 MiB = 36.73 MB | Fedora 固定 d807 源构建成功；未发布 | 无独立目标 |
| ModelDev webhook；同时复用为材料/配置复制 init 镜像 | 1 | 67,703,675 B = 64.57 MiB = 67.70 MB | Fedora 固定 d807 源构建成功；未发布 | 无独立目标 |
| ModelDev training CPU | 1 | UNKNOWN，未构建 | 固定 LLaMA-Factory 基础镜像不在当前 Fedora 缓存 | 无独立目标 |
| ModelDev training GPU | 1 | UNKNOWN，未构建 | 同上；两镜像共享同一固定基础层 | 无独立目标 |
| 固定 LLaMA-Factory 基础镜像 | 1，构建依赖，不是新增平台服务 | UNKNOWN，本轮未取得 registry layer 大小 | 已有 registry digest eb5e106d93e47532e4b72f5995232624b704edea1c1c79468ddcd2ceffa14b4b；未拉取/导入 | 非主服务更新目标 |
| 固定 KFP driver / launcher 支持镜像 | 2 个用途，复用已批准固定版本 | UNKNOWN，本轮未取得大小 | 仍须固定实际 digest、拉取和 TLS 证据；不重建 KFP 平台 | 不属于本次主服务镜像目标 |

两个已构建的附加镜像合计 104,429,413 B = 99.59 MiB = 104.43 MB。这不包含训练基础层、CPU/GPU training、主服务及支持镜像，不能称为总下载量。四个 Pipeline profile 复用两个 training 镜像，不需要为 A/B 或四 profile 各构建一套。

源证据：`build/build-receipt.json`，固定 source d807cbdbd5dd6664d61480acedc752e19cfca48a / tree 141053367fa4be9a6fb9c764b2d7900b5282be20。镜像发布后才可取得 registry 压缩 layer 总量及去重增量；本清单未执行镜像导出、打包或传输。

## 非镜像更新和前置动作

| 交付项 | 精确范围 | 当前状态 / 入口缺口 |
|---|---|---|
| 全局 native 策略与 RBAC bootstrap | 3 VAP、3 Binding、10 ClusterRole、4 ClusterRoleBinding、1 ani-system RoleBinding，共 21 个已渲染对象；按 native 验证后才授 create/bind 的顺序安装，并清掉本 MD 自有过宽旧授权 | 仅 server dry-run；未安装。入口没有对象安装操作 |
| 一个共享 webhook | Deployment、Service、TLS 证书/Secret 引用、ValidatingWebhookConfiguration、同一材料包挂载及 init | 未切换。主 ModelDev image-only 更新不覆盖这些资源 |
| 主服务接线 | 平台配置、同一不可变材料 ConfigMap、普通只读文件 init/EmptyDir 挂载、KFP/Kube 两 audience token 投影、artifact 两键 Secret 文件挂载，移除旧 tenant binding/facts 文件引用 | image-only patch 保留旧挂载/env/args，因此单换镜像不能完成 |
| 四个固定 KFP 定义 | ani-system 下 modeldev-mlp-cpu、modeldev-mlp-gpu、modeldev-sft-lora-cpu、modeldev-sft-lora-gpu 的 Pipeline/Version；原四 root 参数、完整 IR SHA、material SHA、不可变 bundle | 编译/发布工具测试通过；最终实际发布及 ID 未完成。入口无材料发布操作 |
| 专用内部 artifact 存储身份 | 独立 artifact bucket；平台共享 AK/SK 仅此 bucket IAM；源 Secret 引用、HTTPS CA 实际加载/I-O/轮换 | 未准备/未验收；不复用业务管理员凭据。入口无 bucket/IAM 操作 |
| 业务输入和基础模型准备 | 复用已批准存储连接/物理 bucket；新 input/publication/model tenant scope；核验 A/B 合法模型、必要输入与 GPU_SHARED 策略 | 候选公共连接/CA已取；B GPU_SHARED 政策及模型可见性缺证，不能借用 A 资产冒充 |
| 数据库迁移 | ModelDev 环境登记 schema；Gov 新权限/接口所需迁移；Res namespace_uid 小窗口 freeze → 迁移 → 精确两行回填 → 零 NULL → 新 Res → release | 工具/组件已验证；现场全未执行。入口无显式维护迁移/窗口操作 |
| 旧执行退休与清理 | 固定原50 MD/54 Gov 清单；原关闭 fence/writer/PVC/退出/原 GPU event ACK；停旧 worker/replay；按 UID/版本清旧自有 Kube/KFP/artifact；精确 Gov→MD 两事务删旧聚合 | 三条一次性修正已批准，原资格仍在核实；额外 f216、四 UNKNOWN 未获豁免。真实删除0，不能以 image rollout 替代 |
| A/B 显式开通 | 通过新 API 先 A 后 B Ensure/Get；服务自行创建固定 SA/RB/CM、专用 artifact Secret 和 Experiment；保留既有标准 Namespace | NOT_RUN；不是平台手工预建每租户 binding，也不创建 Namespace |
| A/B 真实验收 | 同一共享 Pod、不重启接 B；CPU/GPU_SHARED、Stop/原释放、权限/跨租户隔离、token/credential 轮换、旧资源零与其他 owner 原 UID/健康 | LIVE01–11 未完成；验收不是更新入口支持的发布操作 |

## 当前网络和审批边界

用户给出的 Workbench 主机路径为 `proxychains ssh root@192.168.25.40`。本次只读连接实际返回 `Connection refused`，发生在 SSH 认证前；指定集群 SSH 同期返回 `Connection closed`。未修改代理配置，也未换集群入口。

用户随后明确答复“你先试试1,如果太慢就报告”，已批准主服务镜像继续经入口，其余固定材料与bootstrap/挂载/存储/维护迁移使用Fedora和同一指定集群SSH；见update-entry-scope-approval-20261101.json。若慢报告实际大小/吞吐/耗时，原资格与门禁保持。
