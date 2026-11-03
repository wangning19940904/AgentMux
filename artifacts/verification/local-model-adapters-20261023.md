# TraeX / 豆包工作接入验证（2026-10-23）

已更新并运行 `/Applications/AgentMux.app`，bundle ID 保持 `com.wails.agentmux-desktop`。当前 Codex 与 Claude Code 默认使用豆包工作 `gpt-6.1-sol`，可在「LLM Provider 与路由」切换 TraeX。

| 上游 | 客户端 | 实测模型 | 文件工具 | 两轮会话 / 恢复 |
| --- | --- | --- | --- | --- |
| traex | codex | `DeepSeek-V4-Flash` | 通过 | 通过 |
| traex | claude | `DeepSeek-V4-Flash` | 通过 | 通过 |
| doubao | codex | `gpt-6.1-sol` | 通过 | 通过 |
| doubao | claude | `gpt-6.1-sol` | 通过 | 通过 |

每组首轮读取随机标记、写入文件，第二轮恢复相同会话、追加内容并回读；验证真实文件内容及模型回忆的标记。安装后重新执行四组验证。普通启动的两个 CLI 也使用保存配置返回 `CONFIG_OK`。

协议跟踪确认 TraeX 分别走原生 Responses / Messages；Claude → 豆包工作走 Messages → Responses。豆包状态文件确认 Claude 的会话标识映射有效。

后端测试：`go test ./provider ./server ./core` 通过。网关前端测试：8 项通过。`make desktop` 使用当前 Xcode 对应 SDK 构建成功；新应用通过进程路径、管理 API、代理状态和新预设检查。构建 `.app` 与隐藏临时目录已清理，未新增应用入口。

两个适配器通过用户 LaunchAgent 常驻；豆包调试端口已关闭。当前读到 TraeX 35 个、豆包工作 9 个模型，数量会随账号及上游变化。

未覆盖全部模型、Claude → 豆包的图片/二进制工具结果、超长上下文和长时间审批恢复。Codex 通用目录仍采用保守的 128k 窗口。

详见 [接入与维护说明](../../integrations/local-model-adapters.md) 和 [结构化验证记录](local-model-adapters-20261023.json)。
