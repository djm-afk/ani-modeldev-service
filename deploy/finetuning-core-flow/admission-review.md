历史任务准入材料已由正式统一组件替代

当前唯一 Pod admission 规则在 `internal/server/modeldevwebhook`；正式独立进程为
`cmd/ani-modeldev-webhook`。部署配置和分离注册材料见
[统一服务部署说明](../modeldev-webhook/README.md)。这里的 Python 入口只转交正式
Go 进程或 renderer，不再持有任务日期、namespace 或第二套挂载规则。

2026-10-14 原任务 MWC 注册被用户明确拒绝。原完整清单、摘要、支持对象与
TLS/OCI 失败和恢复回执保留在 ignored records 及 Fedora private 材料。统一服务
源码和新 render 没有改变该授权事实，也没有自动注册或部署行为。
旧共享 `ani-modeldev-exit-retention` 未被改动；存量 Pod 的旧 finalizer 仍由原
ModelDev 关闭契约处理。真实 CORE 运行保持未执行。
