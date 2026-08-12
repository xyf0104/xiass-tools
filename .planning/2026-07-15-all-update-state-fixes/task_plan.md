# All Update State Fixes

## Goal

修复所有更新入口中因缓存状态错误、失败状态被清除或缺少可见反馈而导致的更新立即消失/无响应问题，同时保留现有 ChatGPT Desktop 修复。

## Phases

- [x] Phase 1: 复现并修复 ChatGPT Desktop 的陈旧更新计划与错误进度消失问题
- [x] Phase 2: 盘点 Claude Desktop、通用工具更新和 CodeStudio Lite 自更新状态机
- [x] Phase 3: 为每个确认的问题建立可判红回归测试
- [x] Phase 4: 实施最小范围修复并保留可操作的失败状态
- [x] Phase 5: 运行聚焦测试、全量检查并审阅最终差异

## Constraints

- 不覆盖或回退已有 ChatGPT 修复。
- 不提交 `.planning/`、`.superpowers/` 及根目录工作记录。
- 未经用户要求不提交或推送。
- 原生 Tauri 构建受 D: 剩余空间约 0.41 GB 阻塞，不擅自清理缓存。

## Errors Encountered

| Error | Attempt | Resolution |
|---|---:|---|
| `tauri dev` updater 配置为 null | 1 | 使用生成的 updater 配置后越过初始化 |
| Rust 编译 `os error 112` 磁盘空间不足 | 1 | 停止原生构建，保留为环境阻塞 |
