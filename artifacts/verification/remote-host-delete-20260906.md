# SSH 机器删除修复

- 将 RemoteHostsPanel 的浏览器原生确认改为应用内确认框；提供取消、Escape、键盘焦点管理、删除中状态和错误重试。
- 防止重复提交；成功后才切换已删除机器的选中范围，并立即同步列表与机器选择器；忽略删除前发出的过期列表响应。
- 新增 5 项交互回归测试，模拟原生 window.confirm 不可用。前端 94 项测试全部通过，TypeScript/Vite 构建通过，服务端与 remote 相关测试通过。
- 隔离浏览器验证确认框、取消、处理中和删除后列表更新。
- 桌面生产构建完成，已更新 /Applications/AgentMux.app，签名及二进制 SHA256 校验通过。运行中的应用保留，新界面在下次启动生效。
- 通过本机已认证 API 删除 lemon_claw 的连接记录；API 与持久化文件均核验通过。其余 aliyun-ecs-bj、aliyun-swas-sg、ecs_cn 保留。
- 连接记录与旧桌面程序保存在 ~/Library/Application Support/agentmux/backups/ 下，仅存于本机。
