# 任务分诊状态

在普通任务文件顶部附近，用 `Status:` 记录默认的五个分诊状态之一。
也可以使用 Markdown 写法 `**Status:**`。

| 技能中的标准状态 | 本仓库使用的状态值 | 含义 |
| --- | --- | --- |
| `needs-triage` | `needs-triage` | 需要维护者评估这条需求。 |
| `needs-info` | `needs-info` | 等待需求提出者补充信息。 |
| `ready-for-agent` | `ready-for-agent` | 需求已经明确，可以交给代理处理。 |
| `ready-for-human` | `ready-for-human` | 需要由人工实施。 |
| `wontfix` | `wontfix` | 决定不处理。 |

技能要求“添加分诊标签”时，就把本地任务的状态设置为表中对应的值。
这些状态表示需求的准备程度和处理方式，并不证明实现成功或验收通过。
实际处理结果单独用 `Resolution:` 记录；完成标准及 `/wayfinder` 的独立决策状态见[任务跟踪配置](issue-tracker.md)。
