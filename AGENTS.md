# Service repository guidance

This service owns its generated runtime source. It has no runtime dependency on
the layout that created it.

## Boundaries

- `cmd/<service>` is the explicit composition root.
- `internal/service` adapts inbound transport contracts to use cases.
- `internal/biz` owns domain concepts, behavior, use cases, and required ports;
  it must not import Kratos, protobuf, transports, or storage drivers.
- `internal/data` implements outbound ports and owns infrastructure-specific
  code. Add an adapter only when a vertical slice needs it.
- `internal/server` owns transport and cross-cutting runtime wiring, not
  business rules.

Prefer framework capabilities over local replacements. Do not introduce a
shared ANI runtime module, generalized YAML exception registry, or technology
switches unrelated to a real slice. Generate protobuf output through the pinned
workflow and run `make verify` before committing.

## Agent skills

- 创建或更新任务、规格和待决策问题前，读取[任务跟踪配置](docs/agents/issue-tracker.md)，使用本地 `.scratch/`。
- 分诊普通任务前，读取[分诊状态映射](docs/agents/triage-labels.md)。
- 讨论领域术语或记录 ADR 前，读取[领域文档配置](docs/agents/domain.md)。
