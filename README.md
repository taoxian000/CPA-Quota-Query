# Codex 额度托盘监控

一个轻量级 Windows 系统托盘程序，用于查看 Codex 额度和重置额度信息。程序使用 Go 标准库与 Windows 原生 API，不依赖 Electron，也无需在目标电脑上安装额外运行时。

## 重要依赖：CPA-Plugin-userrouting

**本项目依赖 [CPA-Plugin-userrouting](https://github.com/taoxian000/cpa-plugin-userrouting) 提供的额度查询与重置接口，不是独立的额度查询后端。** 如果 CPA 未加载该插件、插件未启用公开资源接口，或请求中使用的 CPA 下游 API Key 无效，额度查询将无法工作。

使用前需满足：

- CPA 需支持插件所需的 `QuotaProvider` ABI。
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

### 服务器地址

**程序默认请求 `https://kot.xkcht.eu.org`，上述凭据会发送到该地址。** 如需使用自己的 CPA，请先退出程序，再在设置文件 `%APPDATA%\CodexQuotaTrayMonitor\settings.json` 中加入 `ServerURL`（可带路径前缀，不含 `/v0/...` 部分）：

```json
{
  "ServerURL": "https://cpa.example.com"
}
```

`ServerURL` 必须使用 HTTPS；仅 `localhost`、`127.0.0.1` 等本机地址允许使用 HTTP。地址无效时程序不会发送任何请求，并弹窗提示。额度与重置请求都不跟随重定向，以免凭据被转发到其他地址。

## 构建

安装 Go 后运行 `build.bat`，即可在当前目录生成 Windows AMD64 GUI 程序（不会打开控制台窗口）。版本号默认取最近的 Git tag，也可以作为参数指定，例如 `build.bat v1.5`；输出文件名为 `quota-monitor-<版本>.exe`，同名文件会被覆盖。

推送 Git tag 会触发 GitHub Actions：运行测试并构建 Windows AMD64 程序；符合发布条件的 tag 会自动创建或更新 GitHub Release 并附上程序，其他 tag 仅生成保留 30 天的工作流 artifact。自动升级同时兼容旧格式的固定附件名。

## 功能

- 程序启动后默认弹出面板（有保存的位置时显示在该位置，否则显示在托盘图标旁）；再次运行程序会打开已在运行的那个实例的面板，而不会启动第二个实例。
- 托盘图标显示实际账户的 5 小时额度和周额度。图标按系统托盘的实际尺寸逐像素绘制，不经缩放，刷新后不会变模糊。
- 单击托盘图标可显示或隐藏面板；面板可拖动，位置会保存。非置顶模式下，面板失去焦点（切换到其他窗口）时自动隐藏，但弹出确认框或提示框时不会隐藏。
- 面板为每个名义账户显示一张卡片，含额度进度条和重置倒计时；服务端响应的 `actual_accounts` 中每个账户另显示一张实际账户卡片。每次成功刷新都会重新排列卡片，并同步调整窗口高度、重绘面板。任一额度窗口或重置次数到期时都会立即强制刷新。倒计时每分钟更新：不足一小时显示分钟，1 小时至不足 1 天显示小时和分钟，1 天及以上显示天和小时。
- 名义账户的 5 小时或周额度为 0% 时，到达其重置时间会发送 Windows 通知「5h额度已恢复」「周额度已恢复」（同时恢复则为「5h/周额度已恢复」）。每个重置时间只通知一次；如果在重置时间之前额度已经恢复（例如消耗了重置次数），则不通知。
- 名义账户卡片显示可用重置次数、已返回的到期时间和剩余时间。重置按钮位于第一张名义账户卡片上，会二次确认（多个名义账户时列出全部账户），然后调用插件的重置接口，并在返回后弹窗提示结果、刷新额度。
- **重置操作有服务端副作用**：`apikey` 模式调用 `/quota/reset`，会对该 Key 对应名义前缀下符合条件的账户尝试消耗重置额度；`chatgpt` 模式调用 `/quota/direct/reset`，只消耗当前 Codex 账户的一次可用额度。请求超时后程序不会自动重试，以免重复消费。公开重置接口不会清除 CPA 本地额度冷却状态。
- 额度查询遇到网络错误、超时、HTTP 408/429 或 5xx 时最多重试 5 次（含首次请求共最多 6 次），采用递增间隔；401、403、404 等客户端错误不会重试。重置请求不自动重试。
- 失败会弹窗显示完整原因：
  - 手动刷新失败时一定弹窗；自动刷新只在从成功转为失败时弹窗一次，之后连续失败只更新状态栏，恢复成功后重新计数。
  - 重置请求失败或无法确认结果时弹窗。
  - 下载或安装更新失败时弹窗；如果替换程序时失败，下次启动时弹窗说明原因。
  - 手动“检查更新”无论结果如何都会弹窗；每日自动检查失败只更新状态栏。
  - 开机启动设置失败时弹窗。
- 面板支持亮色、暗色和跟随系统主题，以及始终置顶、最小化和退出按钮；右键菜单提供刷新、打开面板、开机启动、刷新间隔设置和退出。面板支持高 DPI 显示，按系统缩放比例清晰绘制。
- 刷新间隔默认为 10 分钟，可设置为 10 分钟、20 分钟、30 分钟或 1 小时。
- 程序启动时及之后每 24 小时检查一次 GitHub 最新正式 Release；托盘右键菜单也可手动检查。发现更新后只在面板和托盘菜单提示，不发送 Windows 通知、不自动打开面板。点击更新后会下载 Windows AMD64 程序并校验 SHA-256，替换原 exe 前再校验一次，然后重启程序；仅当安装目录不可写时请求管理员权限，设置文件和开机启动路径保持不变。
- 所有额度条随剩余百分比从红色渐变至蓝色、绿色。

## 测试

运行 `go test ./...`。有一个可选的冒烟测试会读取真实的 Codex 凭据文件，只发往本机模拟服务器；默认跳过，需将环境变量 `CPA_QUOTA_AUTH_FIXTURE` 设为 `auth.json` 副本的路径才会运行。

面板设置保存在当前用户的应用配置目录；开机启动使用当前用户的 Windows Run 注册表项。
