# Codex 额度托盘监控

一个轻量级 Windows 系统托盘程序，用于查看 Codex 额度和重置额度信息。程序使用 Go 标准库与 Windows 原生 API，不依赖 Electron，也无需在目标电脑上安装额外运行时。

## 重要依赖：CPA-Plugin-userrouting

**本项目依赖 [CPA-Plugin-userrouting](https://github.com/taoxian000/cpa-plugin-userrouting) 提供的额度查询与重置接口，不是独立的额度查询后端。** 如果 CPA 未加载该插件、插件未启用公开资源接口，或请求中使用的 CPA 下游 API Key 无效，额度查询将无法工作。

使用前需满足：

- CPA 版本为 **v7.3.9 或更高版本**，并支持插件所需的 `QuotaProvider` ABI。
- 已安装并加载 `CPA-Plugin-userrouting`，且启用额度提供方及公开资源接口：

  ```yaml
  quota_provider:
    enabled: true
    public_endpoint: true
  ```

- CPA 服务端可访问，且暴露以下插件资源路由：

  ```text
  GET /v0/resource/plugins/user-routing/quota
  GET /v0/resource/plugins/user-routing/quota/direct
  GET /v0/resource/plugins/user-routing/quota/reset
  GET /v0/resource/plugins/user-routing/quota/direct/reset
  ```

程序从当前 Windows 用户的 `%USERPROFILE%\.codex\auth.json` 读取认证信息，并按 `auth_mode` 选择查询方式：`apikey`（以及兼容旧文件的未设置模式）沿用 CPA 下游 API Key 接口；`chatgpt` 则读取 `tokens.access_token` 和 `tokens.account_id`，调用插件的 `/quota/direct` 查询单个账户。ChatGPT 模式重置时调用 `/quota/direct/reset`，使用与查询相同的 Bearer token 和 `ChatGPT-Account-ID` 请求头；两种方式都依赖 CPA 已加载 `CPA-Plugin-userrouting` 并启用了公开额度接口。

`apikey` 模式中的 `OPENAI_API_KEY` 必须是 CPA 接受的下游 Key；仅有普通 OpenAI API Key 并不能替代 CPA 插件或其 API Key 配置。ChatGPT 模式的 access token 会作为 Bearer 凭据发送给 CPA 插件，请确保仅通过 HTTPS 访问，并避免在代理或应用日志中记录认证头。

## 构建

本地安装 Go 1.22 或更高版本后，运行 `build.bat`。脚本会将版本 `v1.3` 嵌入程序，并在当前目录生成 `quota-monitor-v1.3.exe`（Windows AMD64 GUI 程序，不会打开控制台窗口）。发布新版本时同步更新 `build.bat` 中的版本号。

推送 Git tag 会触发 GitHub Actions：运行测试并构建 Windows AMD64 程序，产物名为 `quota-monitor-<tag>.exe`；符合 `vMAJOR.MINOR` 或 `vMAJOR.MINOR.PATCH` 格式的 tag 会自动创建或更新 GitHub Release 并附上该文件。其他 tag 仅生成保留 30 天的工作流 artifact。自动升级同时兼容旧版固定附件名。

## 功能

- 托盘图标显示实际账户的 5 小时额度和周额度。
- 单击托盘图标可显示或隐藏面板；面板可拖动，位置会保存。
- 面板显示名义账户及其额度进度条和重置倒计时；仅当服务端响应中的 `actual_accounts` 是非空账户对象时，才额外显示实际账户卡片。倒计时每分钟更新：不足一小时显示分钟，1 小时至不足 1 天显示小时和分钟，1 天及以上显示天和小时。
- 名义账户区域显示可用重置次数、已返回的到期时间和剩余时间。重置按钮会二次确认，调用插件的重置接口，并在返回后提示结果、刷新额度。
- **重置操作有服务端副作用**：`apikey` 模式调用 `/quota/reset`，会对该 Key 对应名义前缀下符合条件的账户尝试消耗重置额度；`chatgpt` 模式调用 `/quota/direct/reset`，只消耗当前 Codex 账户的一次可用额度。请求超时后程序不会自动重试，以免重复消费。公开重置接口不会清除 CPA 本地额度冷却状态。
- 额度查询失败后最多重试 5 次（含首次请求共最多 6 次），采用递增间隔；重置请求不自动重试。
- 面板支持亮色、暗色和跟随系统主题，以及始终置顶、最小化和退出按钮；右键菜单提供刷新、打开面板、开机启动、刷新间隔设置和退出。
- 刷新间隔默认为 10 分钟，可设置为 10 分钟、20 分钟、30 分钟或 1 小时。
- 程序内置版本号，启动时及之后每 24 小时检查一次 GitHub 最新正式 Release；托盘右键菜单也可手动检查。发现新版本后只在面板和托盘菜单提示，不发送 Windows 通知、不自动打开面板。点击更新后会下载固定名称的 Windows AMD64 程序并校验 SHA-256，再替换原 exe、重启程序；仅当安装目录不可写时请求管理员权限，设置文件和开机启动路径保持不变。
- 所有额度条随剩余百分比从红色渐变至蓝色、绿色。

面板设置保存在当前用户的应用配置目录；开机启动使用当前用户的 Windows Run 注册表项。
