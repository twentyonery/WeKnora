# 课题四设计说明：知识网络与引导式学习（Learning Navigator）

> 选题：WeKnora 开源实战课题 · 课题四（开放探索）
> 目标：将用户长期记忆与 Wiki 知识网络关联，度量用户对每个知识节点的掌握程度，形成"点亮知识网络"的可视化与下一步学习引导的闭环。

---

## 1. 问题定义

WeKnora 已有两套彼此独立的"个人化"基础设施：

- **用户长期记忆**：`memory_items`（事实/偏好/兴趣条目）、`memory_topic_stats`（主题接触计数）、`memory_doc_affinity`（文档使用频次），由对话后台蒸馏维护（`internal/application/service/memory/`）；
- **Wiki 知识网络**：`wiki_pages`（互链页面，含 `in_links/out_links`）、知识图谱（Neo4j 实体关系），由文档蒸馏管线增量合并（`internal/application/service/wiki_ingest*.go`）。

前者回答"这个用户是谁、反复关心什么"，后者回答"这片知识域里有什么、如何组织"。二者没有交集：系统不知道用户在知识网络上**点亮了哪些节点、还剩哪些盲区**，更无法据此引导学习。

本课题补上这一层：**知识网络 × 掌握度 → 引导闭环**。

## 2. 知识节点的定义

### 2.1 粒度选型

| 候选粒度 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| 知识库原文档（knowledge） | 与 `memory_doc_affinity` 直接对齐 | 粒度过粗，一篇文档含多个知识点；文档删除即失锚 | 仅作辅助信号 |
| 图谱实体（Neo4j ENTITY） | 粒度细、语义明确 | 依赖 Neo4j 部署（可选组件，`container.go initNeo4jClient` 未配置时降级 no-op）；实体噪声大 | 不作主粒度 |
| **Wiki 页面** | 面向用户可读（用户能打开页面学习）；`page_type` 已区分 summary/entity/concept/synthesis；互链即天然的网络结构；纯 SQL 存储，无额外依赖 | 比实体粗一层 | **主粒度** |

**设计决策：Wiki 页面为节点，页面 `SourceRefs` 关联的原文档与图谱实体作为锚点。** 关键理由：引导的终点必须是"用户能去读的东西"——Wiki 页面是唯一用户可直接消费的形态；且 `WikiGraphRequest.FamiliarKnowledgeIDs`（`internal/types/wiki_page.go:684`）已示范"记忆→图谱个人覆盖层"模式（节点 `Familiar` 字段），本设计是该模式的推广。

### 2.2 节点标识

```
节点 = (tenant_id, kb_id, wiki_page_slug)
```

页面级去重、版本、别名（`aliases`）均由 Wiki 管线维护，掌握度层不重复处理。页面被重新蒸馏合并（`reduceSlugUpdates`）时 slug 稳定，掌握度无需迁移。

## 3. 掌握度的定义

### 3.1 设计原则：只用可观测行为信号，不用模型主观打分

避免"让 LLM 给用户打个分"的退化路径。所有信号来自系统已记录或可廉价记录的行为事件：

| 信号 | 来源（现有设施） | 含义 | 权重 |
|---|---|---|---|
| S1 检索命中 | `memory_doc_affinity.hits`（≥2 才计入，`MemoryDocAffinityMinHits`）→ 经页面 `SourceRefs` 映射到节点 | 用户反复提问命中该节点覆盖的文档 | 0.35 |
| S2 对话引用 | 新增 `node_exposure` 事件：答案引用的 chunk → `ChunkRefs` → 节点（复用 `RecordAnswerSources` 的调用点，`session/qa.go:1767`） | 用户对话中实际"接触到"该节点内容 | 0.25 |
| S3 主题相关度 | `memory_topic_stats.count` + `memory_items`（kind=interest）主题 → 节点标题/别名匹配 | 用户的兴趣主题覆盖该节点 | 0.15 |
| S4 页面访问 | 新增事件：Wiki 页面浏览埋点（`GET wiki page` handler 记录） | 用户主动学习该页面 | 0.25 |

### 3.2 计算公式

每个节点维护信号计数向量，带**时间衰减**（半衰期 14 天，指数衰减），掌握度归一化到 [0,1]：

```
raw(n) = Σ_s w_s · Σ_e decay(now - e.t)      # 按信号加权的事件衰减和
mastery(n) = 1 - exp(-raw(n) / τ)            # τ 为饱和常数，raw=τ 时 mastery≈0.63
```

- `1 - exp(-x/τ)` 保证单调、有界、边际递减——"看过 10 次"和"看过 100 次"差距应远小于"0 次和 1 次"；
- 衰减使掌握度随疏于接触自然回落，避免一次性突击刷满；
- τ 取该知识库节点 raw 值的 P75（按库自适应，避免冷启动库全员接近 0）。

### 3.3 节点状态

```
mastery ≥ 0.6  → lit（已点亮）
0.2 ≤ m < 0.6  → dim（接触中）
m < 0.2 且存在已点亮邻居（in_links/out_links）→ frontier（盲区/前沿，推荐优先级最高）
m < 0.2 无邻居接触 → dark（未触及）
```

frontier 定义利用网络结构：与已掌握内容相邻的未掌握节点是教育心理学中的"最近发展区"，是推荐的首选目标。

## 4. 关联映射：记忆条目 / 行为事件 → 知识节点

三条映射通道，均为**确定性规则 + 阈值**，LLM 不参与打分：

1. **文档通道**：`memory_doc_affinity (knowledge_id)` → 页面 `SourceRefs` 反向索引（建 `page_source_index(kb_id, knowledge_id, slug)` 表）。一篇文档被多页引用时，各页均计一次弱信号（权重 1/引用页数）。
2. **Chunk 通道**：答案引用 chunk → 页面 `ChunkRefs` 反向索引。同上均摊。
3. **主题通道**：`memory_topic_stats.topic` / interest 记忆主题 → 与节点标题+别名做词法匹配（大小写归一 + 包含匹配，复用 `topic_resolve.go` 的归一化逻辑）。

兜底：匹配不到任何节点的记忆条目不产生信号（宁缺毋滥，防止错误点亮）；设计文档与实现注释中说明此取舍。

## 5. 引导策略（推荐闭环）

```
观测（对话/浏览行为）→ 信号入账 → 掌握度更新 → 推荐 frontier 节点 → 用户学习 → 新观测
```

推荐生成规则（确定性排序，非 LLM 生成）：

1. 候选 = frontier 节点；
2. 排序分 = `0.5 × 邻居点亮数/邻居总数 + 0.3 × S3 主题相关度 + 0.2 × 页面重要性（in_links 数归一化）`；
3. 取 Top-N（默认 5），每个推荐附一条可解释文案："你已掌握《X》《Y》，相邻的《Z》建议下一步了解"——解释性是引导可信赖的关键，也因此坚持规则可计算而非模型生成；
4. 用户点击推荐打开 Wiki 页面 → 触发 S4 事件 → 闭环成立。

## 6. 隐私与多租户

对齐课题基本要求第 4 条：

- 数据表以 `(tenant_id, subject_id)` 为根，仓储层沿用 `memory` 模块的 `scoped()` 强制过滤模式（`repository/memory.go:26`），subject 由 `ResolveScope` 从服务端 context 派生，**不接受客户端传入的用户 ID**（`memory/scope.go:28`）；
- **查看**：`GET /api/v1/learning/profile` 返回本人全部画像（掌握度向量 + 信号明细摘要）；
- **导出**：`GET /api/v1/learning/profile/export` 返回完整 JSON（含每条信号的来源与时间）；
- **删除**：`DELETE /api/v1/learning/profile` 清空本人节点状态与事件，并同步清 `memory_doc_affinity` 中本人贡献的计数引用（只删学习画像，不删原始记忆，二者职责分离；文档中说明该边界）。

## 7. 有效性评估方式

采用**离线回放对照实验**（课题允许的三种方式之一）：

1. 构造种子知识库（~20 篇文档）→ 蒸馏出 Wiki 网络；
2. 生成 2 组模拟用户会话历史：A 组围绕其中 5 个主题反复提问（模拟真实学习轨迹），B 组随机散问；
3. 回放两组历史，分别计算掌握度分布；
4. **成功判据**：
   - 判别力：A 组目标主题节点的 mastery 均值显著高于其余节点（期望 ≥3×）；
   - 推荐质量：A 组用户的 frontier 推荐中，与目标主题相邻节点占比显著高于随机基线；
   - 收敛性：随回放事件增多，A 组目标节点 mastery 单调上升（验证公式性质）。
5. 附加：小型真人试用自查（开发者本人使用，记录点亮感知是否与直觉一致）。

## 8. 实现范围与交付物

| 交付物 | 内容 |
|---|---|
| 本设计说明 | 定义、数据来源、公式、评估（本文档） |
| 后端 | migration（`learning_node_states` / `node_exposure_events` / `page_source_index`）、`learning` service（dig 装配）、4 个 API、`PluginExposureRecorder` 聊天管线插件、页面访问埋点 |
| 前端 | Wiki 浏览器图谱 overlay 扩展（lit/dim/frontier/dark 四态着色，沿用现有 `Familiar` SVG 高亮机制）+ 推荐卡片 |
| 验证 | 回放脚本 + 评估报告（含数据） |
