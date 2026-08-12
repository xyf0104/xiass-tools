# Findings

## ChatGPT Desktop

- 本地缓存版本为 `26.707.6957.0`，实际安装版本为 `26.707.9564.0`，缓存 latest 为 `26.707.8479.0`。
- 页面错误地把全局 `detection` 时间戳当作 ChatGPT 更新计划的新鲜度，导致陈旧 expected version 被后端立即拒绝。
- `runAction` 结束后清除 busy 状态，而 UI 仅在 busy 时显示未完成进度，因此错误进度立即消失。
- 已修改 store/UI 并加入两项回归测试；聚焦测试 20/20、Svelte check 和当时全量 unit 均通过。

## Expanded Scope

- Claude Desktop 确认存在与 ChatGPT 相同形状的复合时间戳判断：初始化和导航时都对 `claudeDesktop` 与 `detection` 取最大值/AND 判断，因此全局扫描可掩盖专属 plan 过期。
- Claude `runAction` 的 catch 只写入页面 error，finally 随即清空 `busyAction`；路由的 `showProgress` 仅允许 busy 或 done，故已有失败进度会消失。
- CodeStudio Lite 自更新已经把安装异常持久化为 `status: error`，仍需检查后端事件与安装器启动路径是否存在静默完成或丢失状态。

## Ranked Hypotheses

1. Claude 与 ChatGPT 同源，同时命中陈旧 plan 和失败进度隐藏。
2. Dashboard 通用工具入口只命中失败反馈隐藏，不存在专属 plan 缓存问题。
3. CodeStudio Lite 自更新问题位于后端安装器启动/事件终态，而非前端 store 清空。
4. 通用后端安装器在 Result error 时没有发出失败 progress 终态，导致多个前端只能依赖各自 catch。

## Inventory Notes

- Dashboard 自己维护 `installingToolId` / `updatingToolId`、install progress logs 和 modal 状态；需要分别检查卡片快速更新与确认弹窗更新的 finally 行为。
- 当前代码库没有独立 `appUpdateStore.test.mjs`，自更新 store 的测试 seam 需要从已有 updater 测试定位或新增静态/模块测试。
- Dashboard 确认在失败后保留 modal、`installError` 和 progress logs；通用工具更新没有复现“进度消失且无错误”的前端状态缺陷。
- CodeStudio Lite store catch 会持久化 `status: error`，Settings 始终渲染该错误；后端在 Burn/DMG helper 启动失败时返回 Err，前端可见。成功 spawn 后才退出应用，属于当前安装器 handoff 契约。
- 自更新现有覆盖位于 `src/lib/appUpdaterIntegration.test.mjs`，偏静态契约测试。

## Confirmed Fixes

- Claude 新增两条 red-capable 静态回归测试；修改前 39/41，两个新增断言稳定失败。
- Claude 现在只使用 `claudeDesktop` 时间戳判断专属更新计划是否过期。
- Claude 安装/更新异常会把现有 progress 转为 `phase: "error"`，写入后端错误消息并清空百分比；路由在 busy 结束后仍显示 error progress。
- 修改后 Claude 聚焦测试 41/41 通过。

## Search Errors

- PowerShell 下给 `rg` 传 Windows glob (`src/lib/*.test.mjs`) 导致路径语法错误；后续改为目录搜索并用 `-g`。
- 双引号内正则被 PowerShell 转义破坏；后续使用单引号或纯文本搜索。
