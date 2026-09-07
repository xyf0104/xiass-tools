<p align="center"><img src="macos/source/frontend/public/xiass-tools-logo.png" alt="XIASS Tools" width="96"></p>

# XIASS Tools

面向 macOS 和 Windows 的多 Agent 本机管理工具。XIASS Tools 是统一母应用，Antigravity WF、Codex、Claude Code、Cursor 与 Windsurf 分别作为独立模块提供各自经过验证的配置、连接、启动、诊断与恢复能力；共享更新、主题、本地验证器、安全存储和脱敏诊断服务。

[![XIASS Tools release](https://img.shields.io/github/v/release/xyf0104/Antigravity-WF-Assistant?label=XIASS%20Tools%20release)](https://github.com/xyf0104/Antigravity-WF-Assistant/releases/latest)
[![License](https://img.shields.io/badge/license-MIT%20%2B%20CC%20BY--NC--SA%204.0-blue.svg)](nextgen/ORIGIN_AND_LICENSE.md)
![macOS Universal](https://img.shields.io/badge/macOS-Universal-5b5b5b)
![Windows x64](https://img.shields.io/badge/Windows-x64-0078d4)

## 已实现的能力

- **Antigravity WF**：自动发现 Antigravity IDE 与 Antigravity 2.x 安装，管理本地代理、自定义上游模型、账户池、模型测试、启动/重启、补丁备份/恢复、历史恢复、自动批准、图片链路和脱敏诊断。上游账户可由用户主动添加、导入或通过支持的 OAuth 流程授权。
- **Codex**：管理本机 `config.toml` 中独立的 `xiass_tools` Provider，支持 API 地址规范化、上游模型发现、原子写入、校验备份/恢复、workspace 状态修复与按需历史兼容修复。账号中心支持用户主动执行 OAuth、API Key / Token / JSON 导入、从本机 Codex 或 ChatGPT Desktop 导入、额度刷新、账号切换与实例配置；只有点击“本机导入”“切换/注入”或启动受保护事务时，才会访问或写回对应的本机凭据、配置或系统凭据位点。对于已验证的 Codex / ChatGPT Desktop，用户可在明确确认后执行“保存、可选历史修复并启动”；不会伪造官方账号、订阅等级或额度。
- **Claude Code**：工作台支持 Claude / Claude Code 的 OAuth、API Key、JSON 与本机登录态导入、额度查询、账号切换、实例配置，以及 `settings.json` 中用户明确的 API 根地址、授权令牌和模型。用户选择导入、切换或启动后，才会处理该流程所需的 Claude 本机配置、登录态或系统凭据；受支持的配置写入会显示相应范围，并在已实现流程中提供校验或恢复保护，凭据不会进入诊断包。
- **Cursor / Windsurf**：账号中心支持用户主动发起 OAuth、Token / JSON / 本机导入、额度刷新、账号切换/注入与实例配置；对应客户端的认证存储只会在用户选择“从本机导入”或“切换/注入”时处理。MCP 配置仍只管理 XIASS Tools 自己保留的条目：Cursor 可通过原生目录选择器明确选择项目 `.cursor/mcp.json`，Windsurf 使用其全局 MCP 配置；已支持恢复点的操作可查看、恢复或删除经校验的恢复点。
- **独立 Agent 模块**：左侧直接列出 Antigravity WF、Codex、Claude Code、Cursor 与 Windsurf。点击哪个 Agent，右侧就只显示该 Agent 的检测、配置、启动、诊断与恢复能力，不再经过重复的平台选择器。Antigravity 的模型、上游账户和权限不会混入其他 Agent；Codex、Claude Code、Cursor 与 Windsurf 也不会被伪装成共享 Antigravity 登录态。对已发现且启动目标再次验证通过的 Cursor / Windsurf 可直接打开应用；前端不能传入路径或命令行。
- **本地验证器（2FA）**：导入标准 `otpauth://totp/` 链接或手动 Base32 密钥；密钥仅保存在 macOS Keychain 或 Windows Credential Manager，可按需显示短时验证码，并能使用密码加密导出或导入恢复。
- **Antigravity 上游账户**：支持 API Key、Bearer / Access Token、`x-api-key`、Setup Token、Codex PAT、自定义认证头、账户凭据 JSON 与 Refresh Token。OpenAI / Codex OAuth 已提供 PKCE、本机自动回调、手动回调兜底、自动刷新和专用 Responses 链路。Grok、Claude、Google/Gemini 与 Antigravity 原生 OAuth 在专用传输尚未完成前不会显示为一键可用；高级 Custom OAuth 仍保留给用户自己的公开客户端与明确上游。这些账户不会读取、替换或导出其他客户端的原生登录会话。
- **更新与诊断**：更新器只接受本项目公开 Release 的标准安装包，并在安装前校验 `SHA256SUMS.txt`。主应用会把自身受管日志与嵌入式 Antigravity WF 的脱敏日志合并为一个诊断 ZIP；诊断包会二次隐藏密钥、Token、Cookie、授权码、邮箱、用户目录和图片数据，不会打包账户、模型、Codex `auth.json` 或聊天历史。

## 下载与安装

请从 [最新发布页](https://github.com/xyf0104/Antigravity-WF-Assistant/releases/latest) 下载与 Release 标签匹配的标准安装包。该公开地址暂时保留历史仓库路径，作为已验证的发布与更新通道，不是 XIASS Tools 的显示名称。当前正式版本为 v1.7.6。面向用户的下载只保留三个标准安装包，不提供 portable、便携版或用户手动解压的 ZIP；Release 中其余签名、清单、校验文件及 macOS `.app.tar.gz` 仅供应用内自动更新使用：

| 系统 | 安装包 | 支持范围 |
| --- | --- | --- |
| macOS | `XIASS-Tools-macOS-universal-v<version>.dmg` | macOS 12 或更高版本，Apple Silicon 与 Intel Mac |
| Windows | `XIASS-Tools-Windows-x64-v<version>.msi` 或 `XIASS-Tools-Windows-x64-v<version>-Setup.exe` | Windows 10/11 x64 |

Windows 安装时可以勾选创建桌面快捷方式。macOS 安装包适用于 macOS 12 或更高版本，安装完成后可从“应用程序”或 Launchpad 打开；若系统拦截未签名应用，请在 Finder 中按住 Control 点按安装包或 App 后选择“打开”，并在“系统设置 → 隐私与安全性”中确认。

从旧版“Antigravity WF助手”升级到 XIASS Tools 时，macOS 会保留旧 App 与原有数据，避免安装程序擅自删除用户文件。请先退出旧 App，确认 XIASS Tools 能正常启动；确认不再需要旧 App 后，再由你手动移至废纸篓。

安装包包含运行时依赖，不需要额外安装 Python、Node.js 或补丁脚本。主 Tauri 外壳的数据默认保存在 `~/.xiass_tools/`，嵌入式 Antigravity WF 的账户、模型与设置仍由 `~/.xiass-tools/` 独立持有；这两个物理目录不会被宣传为同一个存储根。升级时，WF 会以可验证迁移方式合并更早的助手目录；迁移失败时保留并继续使用原目录。设置中的统一账号备份会通过认证本机桥显式包含 WF 的账户、模型供应商和设置，并在恢复前完整校验、失败时回滚。外部 Codex `auth.json` 不会被统一备份或诊断导出自动读取；只有用户在 Codex 工作台明确选择“从本机导入”或“切换/注入”时，才会在对应本机流程中处理它。

## 快速开始

### 配置 Antigravity

1. 打开 XIASS Tools，点击左侧 **Antigravity WF**；本地代理会自动准备，但只有在 Antigravity 工作流需要时才会实际接管连接。
2. 在 Antigravity WF 顶部子导航的“上游”中添加 API Key、Token、账户 JSON 或 OpenAI / Codex OAuth 账户；也可以在“模型”中添加上游、获取模型并完成测试。
3. 回到“总览”，确认已发现的 Antigravity 安装，然后完全退出目标应用，选择“全部连接”或单独连接目标。
4. 连接完成后从首页启动目标应用；使用自定义模型期间保持 XIASS Tools 运行。

### 配置 Codex

1. 点击左侧“Codex”，然后选择“配置”。
2. 输入上游 API 地址和仅用于本次写入的 API Key，获取可用模型后选择默认模型与审查模型。
3. 保存后，XIASS Tools 只写入独立的 `model_providers.xiass_tools` 配置块，并自动创建校验备份。
4. 如出现旧会话兼容问题，再主动选择历史修复；普通保存不会改写本机历史。

如需一次完成“保存、可选历史兼容检查并启动 Codex Desktop”，展开配置窗口中的“高级：安全保存、检查历史并启动 Codex”。该操作要求风险确认；若任一步无法安全验证，XIASS Tools 会停止操作，并在可验证的范围内回滚，不会强制结束 Codex。

### 管理 Codex、Claude Code、Cursor 或 Windsurf 账号

1. 点击左侧对应 Agent，进入“账号中心”。
2. 根据账号来源选择 OAuth、API Key / Token、JSON 或“从本机导入”。本机导入不会自动运行，只有点击该操作后才会读取已选择客户端的相关本机登录资料。
3. 需要使用某个账号时，先检查其状态或额度，再点击“切换/注入”或为实例选择账号。该动作才会将所选账号应用到目标客户端或隔离实例；如页面要求先关闭客户端，请按提示完成。
4. 不需要账号时可从 XIASS Tools 的本地账号库删除。导出账户或跨电脑恢复属于显式敏感操作，请只处理你有权使用的账号，并将导出文件保存在可信位置。

### 配置 Cursor 或 Windsurf MCP

1. 点击左侧“Cursor”或“Windsurf”，然后选择“配置”。
2. 仅在本机已确认客户端且现有 MCP 设置通过安全检查时，输入远程 MCP 地址并保存。Cursor 可额外通过原生目录选择器明确选择一个项目，管理该项目的 `.cursor/mcp.json`；Windsurf 仅使用全局设置。
3. 展开“经过校验的恢复点”可查看本工具为显式保存或恢复操作创建的恢复点；恢复与删除都需要再次确认。

该功能只管理 XIASS Tools 自己保留的 MCP 条目。Cursor 项目路径不会暴露给渲染器，选择会话会短时过期；列表不会展示地址、路径、原始 JSON、请求头、环境变量或认证信息；如果原文件带有敏感配置，XIASS Tools 会把它作为只读保护处理，保持零写入。

### 管理本地验证器

1. 打开“设置 → 验证器（2FA）”。
2. 粘贴标准验证器链接，或输入 Base32 密钥和识别名称。
3. 需要验证码时点击“显示验证码”，再按需复制；密钥不会回显。
4. 迁移到另一台电脑时，先导出加密备份并单独保存密码；在目标电脑选择“导入加密备份”后输入相同密码。备份完整性、密码和条目结构均会验证，重复条目会整批拒绝，不会静默覆盖现有验证器。

## 兼容性与隐私边界

Antigravity 的连接操作按**实际安装结构**验证，而不是承诺任意未知版本都可以安全注入。遇到未识别的结构，XIASS Tools 会保留原文件并提示需要诊断或适配，不会强制覆盖未知 JavaScript、ASAR 或二进制。

默认情况下，XIASS Tools 不会在后台扫描、批量导入或上传其他客户端的账号、浏览器 Cookie、会话、认证数据库或聊天记录。“从本机导入”只在用户点击后读取当前目标 Agent 的必要本机授权资料；“切换/注入”只在用户选择具体账号后写入该客户端或其隔离实例所需的位点。若用户在“设置”中主动开启“本机账号自动导入”，应用才会在本机监听五个已支持 Agent 的登录变更并导入当前身份，可随时关闭；这不会上传原始认证文件、Cookie 或数据库内容。OAuth、模型发现、额度刷新和测试会按用户所选服务连接相应的官方或上游接口；原始本机认证文件、Cookie 和数据库内容不会为了管理功能上传到 XIASS 服务。

所有需要修改本机配置的动作均由用户可见操作触发；支持恢复点的流程会先创建可校验备份，无法安全验证的目标结构会停止写入。已启用的账号也可能按用户开启的刷新、唤醒或实例工作流在本机使用已保存授权，以完成该项已启用的任务；这不等于自动搜集其他应用的账号。只有在用户明确确认的 Codex Desktop 操作中，XIASS Tools 才会启动、请求正常退出或重新打开已验证的 Codex / ChatGPT Desktop；它不会隐式关闭或接管未选择的其他应用。

遇到问题时，请使用“设置 → 诊断与日志 → 导出诊断日志”。请勿上传 API Key、Token、Cookie、账户 JSON、模型配置或未经脱敏的日志。

## 构建与贡献

macOS 与 Windows 的源代码、构建命令和平台说明分别位于：

- [macOS 源码说明](macos/source/README.md)
- [Windows 源码说明](windows/source/README.md)
- [系统架构与能力边界](docs/XIASS-Tools-architecture.md)

## 许可证与声明

本仓库采用混合许可证：XIASS 原有、未标注其他许可证的组件继续按根目录 [MIT License](LICENSE) 发布；`nextgen/` 中从 Cockpit Tools 派生的部分按 [CC BY-NC-SA 4.0 和来源声明](nextgen/ORIGIN_AND_LICENSE.md) 分发；内置 CLIProxyAPI 等独立第三方组件继续适用各自许可证。未经 Cockpit Tools 作者另行书面商业授权，不得把其派生部分用于商业集成、付费分发或企业内部商业用途。

Antigravity、Codex、Claude Code、Cursor 与 Windsurf 的名称及商标归各自权利人所有。XIASS Tools 是独立的本地辅助工具，不代表任何相关产品的官方授权或背书。使用第三方模型服务时，请遵守相应服务条款。
