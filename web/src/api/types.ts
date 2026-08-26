// Shared response/request types for the console REST layer.
export interface Provider {
  id: string;
  name: string;
  preset?: string;
  category?: string;
  base_url: string;
  api_key_env?: string;
  api_key?: string;
  api_key_available?: boolean;
  api_key_issue?: string;
  model?: string;
  extra?: Record<string, string>;
  settings_config?: Record<string, unknown>;
  meta?: Record<string, unknown>;
  enabled: boolean;
  in_failover_queue?: boolean;
  sort_index?: number;
}

export interface ProxyToolConfig {
  tool: string;
  enabled: boolean;
  auto_failover: boolean;
  max_retries: number;
  failure_threshold: number;
  cooldown_seconds: number;
}

export interface ProxyStatus {
  running: boolean;
  base_url: string;
  tools: ProxyToolConfig[];
}

export interface ProxyTrace {
  id: string;
  timestamp: string;
  tool: string;
  provider_id: string;
  provider_name?: string;
  client_protocol: string;
  upstream_protocol: string;
  client_model?: string;
  upstream_model?: string;
  status_code?: number;
  success: boolean;
  error?: string;
  session_id?: string;
  project_dir?: string;
}

export interface ProviderRoute {
  tool: string;
  provider_id: string;
  provider_name?: string;
  base_url?: string;
  api_key_env?: string;
  api_key_available?: boolean;
  api_key_issue?: string;
  model?: string;
  api_format?: string;
  meta?: Record<string, unknown>;
  configured: boolean;
}

export interface ProviderProbeResult {
  ok: boolean;
  url?: string;
  models: string[];
  count: number;
  message: string;
  api_format?: string;
  formats?: ProviderProbeCheck[];
  protocols?: ProviderProbeCheck[];
}

export interface ProviderProbeCheck {
  kind: string;
  name: string;
  ok: boolean;
  url?: string;
  status?: number;
  models?: string[];
  message?: string;
}

export interface ProviderMonitorConfig {
  enabled: boolean;
  interval_minutes: number;
  probe_models: boolean;
  max_models_per_provider: number;
}

export interface ProviderModelHealth {
  model: string;
  state: string;
  status_code?: number;
  message?: string;
  checked_at: string;
}

export interface ProviderMonitorProviderStatus {
  provider_id: string;
  provider_name: string;
  state: string;
  catalog_count: number;
  checked_models: number;
  healthy_models: number;
  unhealthy_models: number;
  added_models?: string[];
  removed_models?: string[];
  models?: ProviderModelHealth[];
  message?: string;
  last_checked_at: string;
}

export interface ProviderMonitorAlert {
  id: string;
  type: string;
  severity: string;
  provider_id: string;
  provider_name: string;
  model?: string;
  models?: string[];
  message?: string;
  created_at: string;
  last_seen_at: string;
}

export interface ProviderMonitorSnapshot {
  config: ProviderMonitorConfig;
  running: boolean;
  last_run_at?: string;
  next_run_at?: string;
  providers: ProviderMonitorProviderStatus[];
  alerts: ProviderMonitorAlert[];
}

export interface Claude3PStatus {
  enabled: boolean;
  configured: boolean;
  config_dir: string;
  profile_path?: string;
  active_profile_id?: string;
  active_profile_name?: string;
  base_url?: string;
  auth_scheme?: string;
  model_count: number;
  provider_id?: string;
  provider_name?: string;
  backup_path?: string;
  message?: string;
}

export interface UsageTotals {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd: number;
  records: number;
}

export interface UsageBucket {
  key: string;
  totals: UsageTotals;
}

export interface ModelStat {
  model: string;
  tokens: number;
  cost_usd: number;
}

export interface SourceStat {
  source: string;
  tokens: number;
  cost_usd: number;
}

export interface AgentStat {
  agent: string;
  tokens: number;
  cost_usd: number;
}

export interface RuntimeStat {
  runtime: string;
  tokens: number;
  cost_usd: number;
}

export interface UsageReport {
  period: string;
  from?: string;
  to?: string;
  timezone?: string;
  totals: UsageTotals;
  buckets: UsageBucket[];
  by_model: ModelStat[];
  by_source: SourceStat[];
  by_agent?: AgentStat[];
  by_runtime?: RuntimeStat[];
}

export interface MenubarSettings {
  icon_theme: string;
  icon_stages: string[];
  icon_metric: string;
  cost_thresholds: number[];
  show_status_icon: boolean;
  show_messages: boolean;
  show_tokens: boolean;
  show_cost: boolean;
  show_cny: boolean;
  currency: "cny" | "usd";
  cny_rate: number;
  breakdowns: string[];
  top_n: number;
}

export interface LaunchAtLoginStatus {
  supported: boolean;
  enabled: boolean;
}

export interface KeepAwakeStatus {
  supported: boolean;
  enabled: boolean;
  duration_minutes: number;
  remaining_seconds: number;
  ends_at?: string;
}

export interface SystemDirectoryEntry {
  name: string;
  path: string;
}

export interface SystemDirectoryListing {
  path: string;
  parent_path?: string;
  entries: SystemDirectoryEntry[];
}

export interface Status {
  ok: boolean;
  projects: number;
  version: string;
}

export interface RemoteHost {
  id: string;
  name: string;
  host: string;
  port: number;
  user: string;
  key_path?: string;
  ssh_alias?: string;
  remote_addr: string;
  api_token?: string;
  api_token_set?: boolean;
  host_key_fingerprint?: string;
  trusted?: boolean;
  clear_api_token?: boolean;
}

export interface DiscoveredRemoteHost {
  name: string;
  host: string;
  port: number;
  user: string;
  key_path?: string;
  ssh_alias: string;
  source: string;
  proxy_jump?: string;
  proxy_command?: boolean;
}

export interface RemoteTestResult {
  ok: boolean;
  name: string;
  latency_ms: number;
  host_key_fingerprint: string;
  status?: Status;
  installed?: boolean;
}

export interface RemoteImportResult extends RemoteTestResult {
  host: RemoteHost;
}

export interface RemoteUpdateResult extends RemoteTestResult {
  previous_version?: string;
  version?: string;
  platform: string;
  arch: string;
  sha256: string;
  data_path: string;
  database_url?: string;
  backup_path?: string;
}

export class RemoteConnectionError extends Error {
  code?: string;
  fingerprint?: string;

  constructor(message: string, code?: string, fingerprint?: string) {
    super(message);
    this.name = "RemoteConnectionError";
    this.code = code;
    this.fingerprint = fingerprint;
  }
}

export interface AgentChannelBinding {
  id: string;
  type: string;
  name?: string;
  chat_id?: string;
  status?: string;
  config?: Record<string, string>;
}

export interface AgentSchedule {
  id: string;
  name: string;
  cron: string;
  prompt: string;
  enabled: boolean;
}

export interface AgentInstance {
  id: string;
  name: string;
  runtime_id: string;
  desktop_thread_id?: string;
  work_dir?: string;
  workspace_mode?: "shared" | "worktree";
  worktree_base_ref?: string;
  session_backend?: "structured" | "tmux";
  system_prompt?: string;
  provider_tool?: string;
  provider_id?: string;
  provider_name?: string;
  default_model?: string;
  default_reasoning_effort?: string;
  default_service_tier?: string;
  default_approval_mode?: string;
  memory_scope?: string;
  env?: Record<string, string>;
  channel_bindings?: AgentChannelBinding[];
  schedules?: AgentSchedule[];
  mcp_servers?: string[];
  skills?: string[];
  clis?: string[];
  enabled: boolean;
  source?: string;
  created_at?: string;
  updated_at?: string;
}

export interface Channel {
  id: string;
  name: string;
  type: string;
  agent_id?: string;
  agent_name?: string;
  default_message_prompt?: string;
  bot_name?: string;
  bot_avatar_url?: string;
  bot_avatar_proxy_url?: string;
  bot_open_id?: string;
  config?: Record<string, string>;
  enabled: boolean;
  state?: string;
  connected?: boolean;
  error?: string;
  started_at?: string;
  connected_at?: string;
  last_checked_at?: string;
  last_heartbeat_at?: string;
  last_event_at?: string;
  last_inbound_at?: string;
  codex_control_capability?: {
    state: string;
    error?: string;
    experimental_api: boolean;
    threads: boolean;
    steer: boolean;
    interrupt: boolean;
    interactions: boolean;
    deep_link: boolean;
  };
  created_at?: string;
  updated_at?: string;
}

export interface ChannelTask {
  id: string;
  channel_id: string;
  conversation_id?: string;
  conversation_key: string;
  chat_id: string;
  user_id: string;
  controller_id: string;
  native_thread_id?: string;
  turn_id?: string;
  status: string;
  error?: string;
  delivery_key?: string;
  delivery_status?: string;
  delivery_attempts?: number;
  delivery_error?: string;
  delivered_at?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  updated_at: string;
}

export interface ChannelConversation {
  id: string;
  scope: string;
  conversation_key: string;
  chat_id: string;
  chat_type?: string;
  agent_id?: string;
  work_dir?: string;
  native_session_id?: string;
  thread_title?: string;
  title?: string;
  message_count: number;
  active_task?: ChannelTask;
  queued_tasks: number;
  controller_id?: string;
}

export interface ChannelInteraction {
  id: string;
  task_id: string;
  channel_id: string;
  conversation_id?: string;
  conversation_key: string;
  controller_id: string;
  nonce: string;
  status: string;
  request: {
    id: string;
    kind: string;
    title?: string;
    description?: string;
    command?: string;
    cwd?: string;
    reason?: string;
    high_risk?: boolean;
    questions?: Array<{
      id: string;
      header?: string;
      question: string;
      secret?: boolean;
      options?: Array<{ label: string; description?: string }>;
    }>;
  };
  created_at: string;
  expires_at?: string;
}

export type MeetingResponseMode = "stream_text" | "final_text" | "text_voice" | "voice";

export interface MeetingChannel {
  channel_id: string;
  channel_name: string;
  platform: string;
  bot_name?: string;
  agent_name?: string;
  response_mode: MeetingResponseMode;
  state: string;
  connected: boolean;
  error?: string;
  target_id?: string;
  target_name?: string;
}

export interface MeetingInvitation {
  id: string;
  nonce: string;
  channel_id: string;
  channel_name: string;
  platform: string;
  meeting_id?: string;
  meeting_number: string;
  topic: string;
  inviter_name: string;
  state: string;
  last_error?: string;
  greeting_sent?: boolean;
  greeting_warning?: string;
  created_at: string;
  expires_at: string;
  target_id?: string;
  target_name?: string;
}

export interface MeetingJoinResult {
  channel_id: string;
  channel_name: string;
  platform: string;
  meeting_id: string;
  meeting_number: string;
  greeting_sent: boolean;
  greeting_warning?: string;
}

export interface MeetingActor {
  id?: string;
  name?: string;
  participant_type?: string;
  role?: string;
}

export interface MeetingTimelineItem {
  id: string;
  meeting_id: string;
  kind: "chat" | "reaction" | "transcript" | "participant_joined" | "participant_left" | "share_started" | "share_ended" | "bot" | string;
  event_time: string;
  actor?: MeetingActor;
  text?: string;
  message_type?: number;
  share_title?: string;
  share_url?: string;
  visibility?: string;
  turn_id?: string;
}

export interface ActiveMeeting {
  id: string;
  meeting_number?: string;
  topic?: string;
  status: string;
  channel_id: string;
  channel_name: string;
  platform: string;
  bot_name?: string;
  agent_name?: string;
  response_mode: MeetingResponseMode;
  joined_at?: string;
  started_at?: string;
  ended_at?: string;
  last_activity_at?: string;
  participant_count?: number;
  target_id?: string;
  target_name?: string;
}

export interface MeetingTurn {
  id: string;
  meeting_id: string;
  question: string;
  source: string;
  status: string;
  error?: string;
  created_at: string;
  started_at?: string;
  ended_at?: string;
}

export interface MeetingDetail {
  meeting: ActiveMeeting;
  items: MeetingTimelineItem[];
  turns: MeetingTurn[];
  target_id?: string;
  target_name?: string;
}

export interface MeetingStreamEvent {
  type?: "meeting.changed" | "meeting.activity" | "meeting.turn" | string;
  channel_id?: string;
  meeting_id?: string;
  meeting?: ActiveMeeting;
  items?: MeetingTimelineItem[];
  turn?: MeetingTurn;
  created_at?: string;
  target_id?: string;
  target_name?: string;
}

export interface MeetingOverview {
  channels: MeetingChannel[];
  invitations: MeetingInvitation[];
  meetings: ActiveMeeting[];
  warnings?: string[];
}

export interface FeishuSetupBeginResponse {
  device_code: string;
  qr_url: string;
  interval: number;
  expires_in: number;
}

export interface FeishuSetupPollResponse {
  status: "pending" | "completed" | "denied" | "expired" | "error";
  base_url?: string;
  app_id?: string;
  app_secret?: string;
  platform?: "feishu" | "lark";
  owner_open_id?: string;
  slow_down?: boolean;
  error?: string;
}

export interface FeishuAutomationBeginResponse {
  session_id: string;
  qr_payload: string;
  expires_in: number;
}

export interface FeishuAutomationPollResponse {
  status: "pending" | "scanned" | "completed" | "expired";
}

export interface FeishuAutomationResult {
  ok: boolean;
  app_id: string;
  scope_count: number;
  missing_scopes: string[];
  events: string[];
  callbacks: string[];
  published: boolean;
  version_id?: string;
}

export interface Trigger {
  id: string;
  name: string;
  kind: string;
  agent_id?: string;
  agent_name?: string;
  channel_id?: string;
  channel_name?: string;
  chat_id?: string;
  cron_expr?: string;
  prompt?: string;
  event?: string;
  action_type?: string;
  action_target?: string;
  token?: string;
  session_mode?: string;
  enabled: boolean;
  hook_path?: string;
  last_run?: string;
  last_status?: string;
  last_error?: string;
  created_at?: string;
  updated_at?: string;
}

export interface MemoryEntry {
  id: string;
  scope: string;
  content: string;
  tags?: string[];
  meta?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface Skill {
  name: string;
  path: string;
  description: string;
  tags?: string[];
  enabled: boolean;
  source?: string;
}

export interface MCPServer {
  name: string;
  transport: string;
  command?: string;
  args?: string[];
  url?: string;
  env?: Record<string, string>;
  enabled: boolean;
}

export interface GuardPolicy {
  id: string;
  tool: string;
  action?: string;
  decision: string;
  priority: number;
}

export interface FrameworkSpec {
  kind: string;
  display: string;
  kind_type: "cli" | "sdk";
  language?: string;
  packages?: string[];
  bin?: string;
  install_supported: boolean;
  install_requires_npm: boolean;
  update_supported: boolean;
  env_required?: string[];
  supported: boolean;
  note?: string;
  internal_only?: boolean;
  install_platforms?: string[];
}

export interface Framework {
  spec: FrameworkSpec;
  installed: boolean;
  version?: string;
  detail?: string;
  registered: boolean;
}

export interface FrameworkPrereqs {
  node: boolean;
  node_path?: string;
  npm: boolean;
  npm_path?: string;
}

export interface FrameworksResponse {
  prereqs: FrameworkPrereqs;
  frameworks: Framework[];
}

export interface FrameworkAuthStatus {
  kind: string;
  state: "authenticated" | "unauthenticated" | "unknown";
  installed: boolean;
  login_supported: boolean;
  detail?: string;
}

export interface FrameworkLoginResult {
  kind: string;
  session_id: string;
  login_url: string;
  verification_code?: string;
  input_required?: boolean;
}

export interface FrameworkInstallResult {
  kind: string;
  action: "install" | "update";
  ok: boolean;
  command?: string;
  log?: string;
  version?: string;
  error?: string;
}

export interface FrameworkUpdateCheck {
  kind: string;
  display?: string;
  installed: boolean;
  current_version?: string;
  latest_version?: string;
  update_available: boolean;
  checked_at?: string;
  error?: string;
}

export interface CLIManagedTool {
  spec: {
    id: string;
    name: string;
    bin: string;
    package: string;
    registry?: string;
    note?: string;
    login_supported?: boolean;
    internal_only?: boolean;
    linked_skills?: CLILinkedSkillSpec[];
  };
  installed: boolean;
  path?: string;
  version?: string;
  detail?: string;
  linked_skills?: CLILinkedSkillStatus[];
}

export interface CLILinkedSkillSpec {
  id: string;
  name: string;
  source?: string;
  match_cli_version?: boolean;
  version_policy_label?: string;
  note?: string;
}

export interface CLILinkedSkillStatus {
  spec: CLILinkedSkillSpec;
  installed: boolean;
  in_sync: boolean;
  path?: string;
  version?: string;
  detail?: string;
}

export interface CLILinkedSkillResult {
  id: string;
  ok: boolean;
  command?: string;
  log?: string;
  path?: string;
  version?: string;
  error?: string;
}

export interface CLIInstallResult {
  id: string;
  action: "install" | "update" | "sync-skills";
  ok: boolean;
  command?: string;
  log?: string;
  version?: string;
  linked_skills?: CLILinkedSkillResult[];
  error?: string;
}

export interface CLIUpdateCheck {
  id: string;
  installed: boolean;
  current_version?: string;
  latest_version?: string;
  update_available: boolean;
  checked_at?: string;
  error?: string;
}

export interface BundleComponentSpec {
  kind: "cli" | "framework";
  id: string;
  name: string;
}

export interface ToolBundle {
  spec: {
    id: string;
    name: string;
    note?: string;
    internal_only?: boolean;
    install_platforms?: string[];
    components: BundleComponentSpec[];
  };
  installed: boolean;
  ready_components: number;
  total_components: number;
  components: Array<{
    spec: BundleComponentSpec;
    installed: boolean;
    ready: boolean;
    version?: string;
    detail?: string;
  }>;
  detail?: string;
}

export interface BundleComponentResult {
  kind: "cli" | "framework";
  id: string;
  ok: boolean;
  skipped?: boolean;
  version?: string;
  command?: string;
  log?: string;
  error?: string;
}

export interface BundleInstallResult {
  id: string;
  ok: boolean;
  components?: BundleComponentResult[];
  error?: string;
}

export interface CLIAuthStatus {
  id: string;
  state: "authenticated" | "unauthenticated" | "setup_required" | "unknown";
  installed: boolean;
  login_supported: boolean;
  detail?: string;
}

export interface CLIAuthSession {
  id: string;
  session_id: string;
  phase: "checking" | "setup" | "login" | string;
  state: "starting" | "waiting" | "succeeded" | "failed" | "cancelled";
  login_url?: string;
  verification_code?: string;
  error?: string;
  started_at?: string;
  updated_at?: string;
}

export interface OperationProgress {
  phase: string;
  detail?: string;
  percent: number;
  started_at?: number;
}

export interface TTSVoice {
	id: string;
	name: string;
	notes?: string;
}

export interface TTSModel {
	id: string;
	name: string;
	description: string;
	languages: string[];
	parameters?: string;
	download_bytes: number;
	license: string;
	engine: string;
	recommended?: boolean;
	voices: TTSVoice[];
	installed: boolean;
	downloading?: boolean;
}

export interface TTSRuntimeStatus {
	version: string;
	supported: boolean;
	installed: boolean;
	download_bytes?: number;
	platform: string;
}

export interface TTSCatalogStatus {
	models: TTSModel[];
	runtime: TTSRuntimeStatus;
}

export interface MarketplaceSkill {
  name: string;
  description: string;
  category?: string;
  source: string;
  repo: string;
  path: string;
  url?: string;
  trusted: boolean;
  installed: boolean;
}

export interface WorkspaceInitResult {
  work_dir: string;
  base_work_dir?: string;
  workspace_mode?: string;
  worktree_branch?: string;
  created?: string[];
  updated?: string[];
  warnings?: string[];
  runtime_id?: string;
  agent_id?: string;
}

export interface ToolsResponse {
  cli: CLIManagedTool[];
  bundles: ToolBundle[];
  frameworks: Framework[];
  skills: Skill[];
  mcp: MCPServer[];
  marketplace: MarketplaceSkill[];
}

export interface AgentSession {
  provider_id: string;
  surface: string;
  session_id: string;
  native_session_id?: string;
  source_kind?: string;
  originator?: string;
  title?: string;
  summary?: string;
  project_dir?: string;
  created_at?: string;
  last_active_at?: string;
  source_path?: string;
  resume_command?: string;
  file_backed: boolean;
  message_count: number;
  messages_partial?: boolean;
  available: boolean;
  status_message?: string;
  origin?: "local" | "channel" | string;
  agent_id?: string;
  agent_name?: string;
  channel_id?: string;
  channel_name?: string;
  channel_type?: string;
  conversation_id?: string;
  conversation_key?: string;
  chat_id?: string;
  chat_type?: string;
  can_chat?: boolean;
	run_status?: string;
	can_stop?: boolean;
	active_task_id?: string;
  terminal_backend?: string;
  terminal_available?: boolean;
  terminal_attach_command?: string;
}

export interface TerminalSessionView {
  info: {
    backend: string;
    session_id: string;
    attach_command?: string;
    available: boolean;
  };
  snapshot: string;
}

export interface ChannelFeedback {
  id: string;
  task_id: string;
  channel_id: string;
  conversation_id?: string;
  user_id: string;
  semantic: "positive" | "progress" | "negative";
  reason?: string;
  comment?: string;
  created_at: string;
  updated_at: string;
}

export interface FeedbackReport {
  items: ChannelFeedback[];
  counts: Record<"positive" | "progress" | "negative", number>;
  total: number;
}

export interface OrchestrationTask {
  id: string;
  orchestration_id?: string;
  agent_id?: string;
  project?: string;
  input: string;
  depends_on?: string[];
  status: string;
  output?: string;
  error?: string;
  invocation_id?: string;
  conversation_id?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  updated_at: string;
}

export interface Orchestration {
  id: string;
  name: string;
  status: string;
  max_concurrency: number;
  error?: string;
  tasks?: OrchestrationTask[];
  created_at: string;
  started_at?: string;
  finished_at?: string;
  updated_at: string;
}

export interface SessionMessage {
  role: string;
  kind?: string;
  content: string;
  timestamp?: string;
  tool_name?: string;
  tool_call_id?: string;
  tool_input?: string;
  tool_output?: string;
  tool_status?: string;
}

export interface ObservationError {
  code?: string;
  message?: string;
  retryable?: boolean;
}

export interface ObservationModel {
  provider?: string;
  requested?: string;
  resolved?: string;
  protocol?: string;
  request_id?: string;
  attempt?: number;
  reasoning_effort?: string;
  service_tier?: string;
  finish_reason?: string;
  ttft_ms?: number;
  duration_ms?: number;
}

export interface ObservationTool {
  name?: string;
  call_id?: string;
  category?: string;
  duration_ms?: number;
  input_bytes?: number;
  output_bytes?: number;
}

export interface ObservationUsage {
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  reasoning_tokens?: number;
  tool_tokens?: number;
  total_tokens?: number;
  cost_usd?: number;
  cumulative?: boolean;
}

export interface ObservationPayloadRef {
  id: string;
  storage?: string;
  content_type?: string;
  key_id?: string;
  original_bytes?: number;
  stored_bytes?: number;
  redacted?: boolean;
  expires_at?: string;
  source_path?: string;
  source_offset?: number;
  source_length?: number;
  source_identity?: string;
  source_sha256?: string;
  source_runtime?: string;
  source_class?: string;
  source_content_sha256?: string;
  content_sha256?: string;
}

export interface ObservationTrace {
  trace_id: string;
  root_span_id?: string;
  name?: string;
  started_at: string;
  ended_at?: string;
  agent_id?: string;
  agent_name?: string;
  runtime_id?: string;
  conversation_id?: string;
  session_id?: string;
  turn_id?: string;
  source?: string;
  provenance?: string[];
  quality?: string;
  status?: string;
  error?: ObservationError;
  model?: ObservationModel;
  usage?: ObservationUsage;
  span_count?: number;
  event_count?: number;
  attributes?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
}

export interface ObservationSpan {
  span_id: string;
  trace_id: string;
  parent_span_id?: string;
  kind: string;
  name?: string;
  sequence?: number;
  started_at: string;
  ended_at?: string;
  duration_ms?: number;
  agent_id?: string;
  runtime_id?: string;
  conversation_id?: string;
  session_id?: string;
  turn_id?: string;
  source?: string;
  provenance?: string[];
  quality?: string;
  status?: string;
  error?: ObservationError;
  model?: ObservationModel;
  tool?: ObservationTool;
  payload_id?: string;
  payload_ref?: ObservationPayloadRef;
  usage?: ObservationUsage;
  attributes?: Record<string, unknown>;
  content?: unknown;
  tool_input?: unknown;
  tool_output?: unknown;
}

export interface ObservationEvent {
  version?: string;
  event_id: string;
  sequence?: number;
  time: string;
  trace_id: string;
  span_id: string;
  parent_span_id?: string;
  kind: string;
  name?: string;
  lifecycle?: string;
  source?: string;
  provenance?: string[];
  quality?: string;
  status?: string;
  error?: ObservationError;
  model?: ObservationModel;
  tool?: ObservationTool;
  usage?: ObservationUsage;
  payload_ref?: ObservationPayloadRef;
  attributes?: Record<string, unknown>;
  content?: unknown;
}

export interface ObservationTraceDetail {
  trace: ObservationTrace;
  spans: ObservationSpan[];
  events: ObservationEvent[];
}

export interface ObservationCoverage {
  source: string;
  quality?: string;
  status?: string;
  events?: number;
  traces?: number;
  last_seen_at?: string;
  detail?: string;
}

export interface ObservationOverview {
  traces?: number;
  spans?: number;
  events?: number;
  model_requests?: number;
  tool_calls?: number;
  failed_traces?: number;
  partial_traces?: number;
  active_agents?: number;
  error_rate?: number;
  usage?: ObservationUsage;
  coverage?: ObservationCoverage[];
  recent_traces?: ObservationTrace[];
}

export interface ObservationInsight {
  id: string;
  rule_id: string;
  agent_id?: string;
  trace_id?: string;
  severity?: string;
  status: string;
  title: string;
  summary?: string;
  suggestion?: string;
  sample_size?: number;
  confidence?: number;
  estimated_token_savings?: number;
  estimated_cost_savings_usd?: number;
  related_trace_ids?: string[];
  only_suggestion?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface ObservationExporterSettings {
  name?: string;
  type?: string;
  endpoint?: string;
  enabled?: boolean;
  include_content?: boolean;
  status?: string;
  pending?: number;
  last_error?: string;
}

export interface ObservationSettings {
  enabled?: boolean;
  capture_content?: string;
  content_retention_days?: number;
  detail_retention_days?: number;
  backfill_days?: number;
  metadata_only?: boolean;
  key_status?: string;
  exporters?: ObservationExporterSettings[];
}

export interface ObservationIntegrationCoverage {
  enabled?: boolean;
  available?: boolean;
  status?: string;
  quality?: string;
  detail?: string;
  last_seen_at?: string;
}

export interface ObservationIntegration {
  host: string;
  name?: string;
  status?: string;
  installed?: boolean;
  version?: string;
  trust?: string;
  pending_trust?: boolean;
  plugin?: ObservationIntegrationCoverage;
  otel?: ObservationIntegrationCoverage;
  transcript?: ObservationIntegrationCoverage;
  proxy?: ObservationIntegrationCoverage;
  owners?: string[];
  owner?: string;
  drift?: boolean;
  conflicts?: string[];
  warnings?: string[];
  target_paths?: string[];
  install_id?: string;
  coverage?: Record<string, string>;
  findings?: ObservationIntegrationFinding[];
  updated_at?: string;
  metadata?: Record<string, unknown>;
}

export interface ObservationIntegrationFinding {
  code?: string;
  severity?: string;
  message: string;
  owner?: string;
  path?: string;
  blocking?: boolean;
}

export interface ObservationIntegrationAction {
  kind?: string;
  target?: string;
  command?: string[];
  reason?: string;
}

export interface ObservationIntegrationActionResult {
  ok?: boolean;
  host?: string;
  action?: string;
  status?: string;
  message?: string;
  pending_trust?: boolean;
  warnings?: string[];
  conflicts?: string[];
  changes?: string[];
  integration?: ObservationIntegration;
  preview?: unknown;
  changed?: boolean;
  blocked?: boolean;
  actions?: ObservationIntegrationAction[];
  findings?: ObservationIntegrationFinding[];
  preserved?: string[];
  record?: Record<string, unknown>;
}

export interface ObservationTraceFilters {
  agentID?: string;
  runtimeID?: string;
  sessionID?: string;
  status?: string;
  source?: string;
  limit?: number;
  offset?: number;
}
