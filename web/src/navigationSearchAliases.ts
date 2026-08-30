// Search vocabulary is intentionally curated instead of generated at runtime:
// short pinyin initials and product terminology are ambiguous, so explicit
// aliases give operators predictable results in both supported UI languages.
export const NAVIGATION_SEARCH_ALIASES = {
  overview: [
    "总览", "概览", "首页", "仪表盘", "控制台", "看板",
    "zonglan", "zong lan", "zl",
    "overview", "home", "dashboard", "summary", "console",
  ],
  agents: [
    "智能体", "助手", "机器人", "AI 助手", "代理",
    "zhinengti", "zhi neng ti", "znt",
    "agent", "agents", "assistant", "bot", "ai agent",
  ],
  orchestrations: [
    "编排", "多智能体", "多 Agent", "协作", "工作流", "流程", "任务编排",
    "bianpai", "bian pai", "bp", "duozhinengti", "duo zhi neng ti", "dznt",
    "orchestration", "orchestrations", "workflow", "multi agent", "agent team", "coordination",
  ],
  frameworks: [
    "框架", "智能体框架", "开发框架", "运行时", "SDK",
    "kuangjia", "kuang jia", "kj", "yunxingshi", "yun xing shi", "yxs",
    "framework", "frameworks", "runtime", "agent sdk", "development kit",
  ],
  skills: [
    "技能", "能力", "插件", "扩展", "技能市场", "工具市场",
    "jineng", "ji neng", "jn", "chajian", "cha jian", "cj",
    "skill", "skills", "plugin", "plugins", "extension", "capability", "marketplace",
  ],
  mcp: [
    "MCP", "MCP 服务", "工具服务", "模型上下文协议", "注册表", "服务注册",
    "zhucebiao", "zhu ce biao", "zcb", "gongjufuwu", "gong ju fu wu", "gjfw",
    "model context protocol", "mcp server", "tool server", "registry", "tools protocol",
  ],
  memory: [
    "记忆", "知识", "知识库", "上下文", "存储", "长期记忆",
    "jiyi", "ji yi", "jy", "zhishi", "zhi shi", "zs",
    "memory", "memories", "knowledge", "context", "storage", "long term memory",
  ],
  sessions: [
    "会话", "对话", "聊天", "任务", "线程", "历史会话",
    "huihua", "hui hua", "hh", "duihua", "dui hua", "dh",
    "session", "sessions", "chat", "conversation", "thread", "task history",
  ],
  meetings: [
    "会议", "视频会议", "通话", "妙记", "会议纪要", "会议助手",
    "huiyi", "hui yi", "hy", "shipinhuiyi", "shi pin hui yi", "sphy",
    "meeting", "meetings", "video call", "conference", "minutes", "vc",
  ],
  channels: [
    "渠道", "连接器", "接入", "消息渠道", "集成",
    "qudao", "qu dao", "qd", "jicheng", "ji cheng", "jc",
    "channel", "channels", "connector", "integration",
    "飞书", "feishu", "lark", "slack", "telegram", "discord", "钉钉", "dingtalk",
  ],
  schedules: [
    "定时任务", "定时", "计划任务", "周期任务", "Cron",
    "dingshirenwu", "ding shi ren wu", "dsrw", "dingshi", "ding shi", "ds",
    "schedule", "schedules", "scheduled task", "cron", "recurring task",
  ],
  triggers: [
    "触发器", "触发", "Webhook", "事件回调", "回调", "自动化",
    "chufaqi", "chu fa qi", "cfq", "chufa", "chu fa", "cf", "huidiao", "hui diao", "hd",
    "trigger", "triggers", "webhook", "event callback", "callback", "automation",
  ],
  gateway: [
    "LLM Provider", "模型服务商", "模型供应商", "服务商", "供应商", "模型服务", "模型厂商",
    "模型健康", "接口健康", "健康检查", "网关", "路由", "连接路由", "代理", "转发", "模型路由", "流量入口",
    "wangguan", "wang guan", "wg", "luyou", "lu you", "ly",
    "fuwushang", "fu wu shang", "fws", "gongyingshang", "gong ying shang", "gys",
    "gateway", "router", "routing", "route", "proxy", "forwarder", "model router",
    "provider", "providers", "vendor", "model provider", "provider health", "health check", "llm api",
    "openai", "anthropic", "claude", "gemini", "google ai", "deepseek", "豆包", "doubao", "火山方舟", "ark",
    "openrouter", "azure openai", "bedrock", "vertex ai", "qwen", "通义千问", "dashscope", "kimi", "moonshot",
    "minimax", "groq", "ollama",
  ],
  observability: [
    "可观测性", "监控", "日志", "链路", "追踪", "指标", "遥测", "告警",
    "keguancexing", "ke guan ce xing", "kgcx", "jiankong", "jian kong", "jk",
    "observability", "monitoring", "monitor", "logs", "logging", "trace", "tracing", "metrics", "telemetry", "alert",
    "opentelemetry", "otel",
  ],
  usage: [
    "用量", "账本", "账单", "费用", "成本", "消耗", "Token 用量", "额度",
    "yongliang", "yong liang", "yl", "zhangben", "zhang ben", "zb",
    "usage", "ledger", "billing", "bill", "cost", "spend", "consumption", "tokens", "quota",
  ],
  feedback: [
    "反馈", "答案反馈", "评价", "意见", "评分", "点赞", "差评", "复核",
    "fankui", "fan kui", "fk", "pingjia", "ping jia", "pj",
    "feedback", "rating", "review", "evaluation", "opinion", "thumbs up", "thumbs down",
  ],
  guard: [
    "守卫", "安全", "权限", "策略", "审批", "访问控制", "工具权限", "风控",
    "shouwei", "shou wei", "sw", "quanxian", "quan xian", "qx",
    "guard", "security", "permission", "permissions", "policy", "approval", "access control", "risk control",
  ],
  machines: [
    "远程机器", "机器", "主机", "服务器", "电脑", "远端", "SSH 主机",
    "yuanchengjiqi", "yuan cheng ji qi", "ycjq", "jiqi", "ji qi", "jq",
    "machine", "machines", "host", "server", "computer", "remote", "remote host", "ssh",
  ],
  tenants: [
    "租户", "组织", "企业", "工作空间", "空间", "账号范围",
    "zuhu", "zu hu", "zh", "zuzhi", "zu zhi", "zz",
    "tenant", "tenants", "organization", "organisation", "org", "workspace", "account scope",
  ],
  settings: [
    "设置", "偏好设置", "配置", "系统设置", "菜单栏", "开机启动", "保持唤醒",
    "shezhi", "she zhi", "sz", "peizhi", "pei zhi", "pz",
    "settings", "setting", "preferences", "preference", "config", "configuration", "menu bar", "menubar",
    "launch at login", "keep awake",
  ],
} as const;

export type NavigationTabID = keyof typeof NAVIGATION_SEARCH_ALIASES;

export const NAVIGATION_GROUP_SEARCH_ALIASES: Record<string, ReadonlyArray<string>> = {
  agents: [
    "智能体", "AI", "zhinengti", "zhi neng ti", "znt", "agent", "agents", "assistant",
  ],
  connectivity: [
    "连接与自动化", "连接与集成", "自动化", "触发器", "渠道", "会议", "Webhook", "接入",
    "lianjieyuzidonghua", "lian jie yu zi dong hua", "ljyzdh",
    "connectivity", "connection", "automation", "integration", "integrations", "trigger", "meeting",
  ],
  operations: [
    "运行与分析", "运维治理", "运行", "分析", "会话记录", "用量", "监控", "日志", "记录",
    "yunxingyufenxi", "yun xing yu fen xi", "yxyfx",
    "runtime analytics", "analytics", "operations", "ops", "governance", "monitoring", "session history", "usage",
  ],
  system: [
    "系统", "系统管理", "xitong", "xi tong", "xt",
    "system", "administration", "infrastructure", "system management",
  ],
};
