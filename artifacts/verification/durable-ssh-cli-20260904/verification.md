# SSH 与 GitHub CLI 更新修复验证

日期：2026-09-04。

## 修复内容

- SSH：将启动等待从“收到首个 HTTP 响应”改为“OpenSSH 确认 stdio 转发通道已建立”。空闲连接、慢接口和长连接不会占住后续隧道的启动位置。启动取消会关闭进程并释放位置；读写 deadline 生效。覆盖 macOS 与 Linux 的 OpenSSH 日志格式，保持主机密钥校验。
- 告警：同一机器的超时去重；框架页面不重复展示全局机器告警；失败操作按错误样式展示。切换页面会清除旧页面的告警并忽略其迟到响应。
- GitHub CLI：Linux 根据现有安装来源选择系统包管理器或官方发行包。发行包分为 256 KiB 分段，以最多 6 条独立连接下载；单段失败最多重试 3 次。完整 SHA256 校验及候选程序版本校验通过后，才原子替换原程序。

## 验证

- 修复前，新增“空闲隧道不阻塞后续请求”与“启动取消释放位置”测试均失败；修复后通过。
- `go test -race ./remote ./tools` 通过。
- Linux 实机执行 OpenSSH 回归测试通过，包含密钥校验、临时文件清理、空闲隧道、取消、日志分片和读超时。
- `AGENTMUX_TEST_SSH_ALIAS=lemon_claw go test ./remote -run '^TestLiveSSHConcurrentRequestsWithIdleTunnel$' -v -count=1` 通过：保留空闲隧道时，6 个并发 HTTP 请求全部成功，约 1.2～2.9 秒。
- 更新后的桌面进程再次执行 3 组并发 fleet 查询，6 个 status/frameworks 请求全部成功，单次约 0.4～2.1 秒。
- `AGENTMUX_TEST_RELEASE_VERSION=2.100.0` 的 Linux 实机安装测试通过：自动下载 15,152,253 字节、验证校验和、安装至隔离临时目录并执行版本检查，共 146.28 秒。没有手工传入发行包。原始日志保存在本目录的 `linux-release-install.log`。
- 前端 84 项测试通过，TypeScript/Vite 构建通过。
- `make desktop VERSION=0.1.8` 完成，包含 Linux/macOS × amd64/arm64 四个平台的远端程序。

## 部署核验

- 已更新 `/Applications/AgentMux.app`，签名校验通过；桌面可正常打开 lemon_claw 的框架目录，机器状态正常。
- 桌面主程序 SHA256：`0b88c3c063bb421adfb725992d14744d3634b8ee59a6b600082546d009410282`。
- 已更新 aliyun-ecs-bj 的 AgentMux 服务，服务健康；GitHub CLI 更新接口返回 `ok: true`，当前版本与最新版本均为 `2.100.0`。
- 远端部署程序与随包 Linux amd64 程序的 SHA256 一致：`d78ac2237fb3da36e3110d42a7cad5e44aa43d88dca0377036178831e8626855`。
- 旧桌面程序与远端程序已保留回滚备份。

两个显式开启的实机测试平时会跳过，不会在普通测试中读取个人 SSH 配置或下载外部发行包。
