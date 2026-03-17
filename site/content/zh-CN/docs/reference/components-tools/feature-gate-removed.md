---
title: "特性门控（已移除）"
linkTitle: "特性门控（已移除）"
weight: 10
---

此页面包含已被移除的特性门控列表，其中信息供参考使用。
被移除的特性门控与已 GA 或已弃用的特性门控不同，因为被移除的特性门控不再被视为有效的特性门控。
然而，已 GA 或已弃用的特性门控仍然被相应的 Kueue 组件识别，尽管它们无法在集群中引起任何行为差异。

对于仍被 Kueue 组件识别的特性门控，请参阅
[Alpha/Beta](/docs/installation/#feature-gates-for-alpha-and-beta-features)
特性门控表或
[Graduated/Deprecated](/docs/installation/#feature-gates-for-graduated-or-deprecated-features)
特性门控表。

### 已移除的特性门控

在以下表格中：

- "From" 列包含了特性引入或其发布阶段更改时的 Kueue 版本。
- "To" 列（如果不为空）包含了你仍然可以使用一个特性门控的最后一个 Kueue 版本。
  如果特性阶段为 "Deprecated" 或 "GA"，"To" 列是特性被移除时的 Kueue 版本。

{{< feature-gates-removed-table >}}
