# 犀牛鸟实战提交说明（课题二 · 课题四）

本仓库在同一源码树上完成两个课题，标签 `rhino-2026-final-二` 与 `rhino-2026-final-四` 均指向该完整可构建状态。总览见 [submission.yaml](submission.yaml)。

## 课题二：可视化沙箱工作台

| 能力 | 入口 |
|---|---|
| 本地子进程后端（补齐 Cube/E2B/Docker/Local 四后端统一接口） | `internal/sandbox/local_subprocess_client.go`、`local_pty_*.go` |
| 会话文件浏览 REST API（列目录/上传/下载/建目录/重命名/删除） | `internal/handler/session/sandbox_files.go`、`internal/sandbox/session_file_browser.go` |
| 前端文件管理器（面包屑、上传、下载、重命名、删除、新建目录） | `frontend/src/views/chat/components/ChatFilesPanel.vue` |
| 产物类型标记 + 页内预览（PPT / 表格 / 网页隔离 iframe / 文档 / PDF / 音视频 / 图表） | `frontend/src/views/chat/components/ChatArtifactsPanel.vue`、`frontend/src/components/document-preview.vue` |
| 演示文稿生成 Skill（python-pptx，产物写 `/workspace/output` 即入产物面板） | `examples/skills/presentation-gen/` |
| 跨会话沙箱限额（每工作区×配置并发上限，超限快速失败，销毁回补） | `internal/sandbox/tenant_sandbox_limiter.go`、`session_lifecycle.go`、设置抽屉 `max_concurrent_sandboxes` |
| 沙箱审计事件（终端打开/开通/限额拒绝/文件增删改） | `internal/types/audit_log.go`（`sandbox.*`）、`sandbox_terminal_service.go` |

界面入口：会话右侧沙箱面板 → **产物 / 工作区 / 终端 / 桌面**。

### 验证

```bash
go build ./...
go test ./internal/sandbox/ ./internal/application/service/
cd frontend && npm test && npx vue-tsc --build
```

## 课题四：知识网络与引导式学习（Learning Navigator）

以 Wiki 页面为知识节点，联动用户长期记忆构建个人知识网络；掌握度只用可观测行为信号（检索命中 / 对话引用 / 主题相关 / 页面访问），带 14 天半衰期衰减，归一化到 [0,1]，形成“点亮知识网络 + 下一步学习引导”闭环。

| 能力 | 入口 |
|---|---|
| 领域模型与存储 | `internal/types/learning.go`、`internal/application/repository/learning.go`、`migrations/versioned/000094_learning_navigator.*.sql` |
| 掌握度计算与学习引导服务 | `internal/application/service/learning/` |
| REST API | `internal/handler/learning.go`、`internal/router/routes_learning.go` |
| 前端可视化与导航 | `frontend/src/views/learning/`、`frontend/src/api/learning/` |
| 设计说明与评估 | `topic4-learning-navigator/docs/DESIGN.md`、`topic4-learning-navigator/evaluation/replay_eval.py`（仓库外随附材料） |
