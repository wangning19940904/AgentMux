
CREATE TABLE IF NOT EXISTS providers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	preset TEXT,
	category TEXT,
	base_url TEXT,
	api_key_env TEXT,
	model TEXT,
	tools TEXT,
	extra TEXT,
	settings_config TEXT,
	meta TEXT,
	enabled BIGINT DEFAULT 0,
	created_at TEXT,
	updated_at TEXT
);
CREATE TABLE IF NOT EXISTS active_provider (
	tool TEXT PRIMARY KEY,
	provider_id TEXT,
	meta TEXT
);
CREATE TABLE IF NOT EXISTS usage_records (
	source TEXT,
	session_id TEXT,
	project TEXT,
	model TEXT,
	timestamp TEXT,
	input_tokens BIGINT,
	output_tokens BIGINT,
	cache_read_tokens BIGINT,
	cache_write_tokens BIGINT,
	tool TEXT,
	cost_usd DOUBLE PRECISION,
	host TEXT,
	provenance TEXT,
	provenance_rank BIGINT DEFAULT 0,
	token_quality TEXT DEFAULT 'exact',
	cost_kind TEXT DEFAULT 'calculated',
	PRIMARY KEY (source, session_id, timestamp, host)
);
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT
);
CREATE TABLE IF NOT EXISTS memory_entries (
	id TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	content TEXT NOT NULL,
	tags TEXT,
	meta TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_memory_scope ON memory_entries(scope);
CREATE TABLE IF NOT EXISTS mcp_servers (
	name TEXT PRIMARY KEY,
	transport TEXT NOT NULL,
	command TEXT,
	args TEXT,
	url TEXT,
	env TEXT,
	enabled BIGINT DEFAULT 0
);
CREATE TABLE IF NOT EXISTS guard_policies (
	id TEXT PRIMARY KEY,
	tool TEXT NOT NULL,
	action TEXT,
	decision TEXT NOT NULL,
	priority BIGINT DEFAULT 0
);
CREATE TABLE IF NOT EXISTS skill_states (
	name TEXT PRIMARY KEY,
	enabled BIGINT NOT NULL DEFAULT 1,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_instances (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	runtime_id TEXT NOT NULL,
	desktop_thread_id TEXT,
	work_dir TEXT,
	workspace_mode TEXT,
	worktree_base_ref TEXT,
	session_backend TEXT,
	system_prompt TEXT,
	provider_tool TEXT,
	provider_id TEXT,
	default_model TEXT,
	default_reasoning_effort TEXT,
	default_service_tier TEXT,
	default_approval_mode TEXT,
	memory_scope TEXT,
	env TEXT,
	channel_bindings TEXT,
	schedules TEXT,
	mcp_servers TEXT,
	skills TEXT,
	clis TEXT,
	enabled BIGINT DEFAULT 1,
	source TEXT,
	owner_tenant_id TEXT,
	visibility TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_agent_instances_owner ON agent_instances(owner_tenant_id);
CREATE TABLE IF NOT EXISTS proxy_config (
	tool TEXT PRIMARY KEY,
	enabled BIGINT DEFAULT 0,
	auto_failover BIGINT DEFAULT 0,
	max_retries BIGINT DEFAULT 3,
	failure_threshold BIGINT DEFAULT 4,
	cooldown_seconds BIGINT DEFAULT 60
);
CREATE TABLE IF NOT EXISTS proxy_live_backup (
	tool TEXT PRIMARY KEY,
	original_config TEXT NOT NULL,
	backed_up_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS proxy_traces (
	id TEXT PRIMARY KEY,
	timestamp TEXT NOT NULL,
	tool TEXT,
	provider_id TEXT,
	provider_name TEXT,
	client_protocol TEXT,
	upstream_protocol TEXT,
	client_model TEXT,
	upstream_model TEXT,
	status_code BIGINT,
	success BIGINT DEFAULT 0,
	error TEXT,
	session_id TEXT,
	project_dir TEXT
);
CREATE INDEX IF NOT EXISTS idx_proxy_traces_time ON proxy_traces(timestamp);
CREATE INDEX IF NOT EXISTS idx_proxy_traces_tool_time ON proxy_traces(tool,timestamp);
CREATE INDEX IF NOT EXISTS idx_proxy_traces_session_time ON proxy_traces(session_id,timestamp);
CREATE TABLE IF NOT EXISTS channels (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	agent_id TEXT,
	config TEXT,
	enabled BIGINT DEFAULT 0,
	owner_tenant_id TEXT,
	visibility TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_channels_owner ON channels(owner_tenant_id);
CREATE TABLE IF NOT EXISTS tenants (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	kind TEXT,
	status TEXT NOT NULL,
	note TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_name ON tenants(name);
CREATE TABLE IF NOT EXISTS tenant_tokens (
	id TEXT PRIMARY KEY,
	tenant_id TEXT NOT NULL,
	name TEXT,
	token_hash TEXT NOT NULL,
	prefix TEXT,
	created_at TEXT,
	last_used_at TEXT,
	expires_at TEXT,
	revoked_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_tokens_hash ON tenant_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_tenant_tokens_tenant ON tenant_tokens(tenant_id);
CREATE TABLE IF NOT EXISTS resource_grants (
	tenant_id TEXT NOT NULL,
	resource_type TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	level TEXT NOT NULL,
	created_at TEXT,
	updated_at TEXT,
	PRIMARY KEY (tenant_id, resource_type, resource_id)
);
CREATE INDEX IF NOT EXISTS idx_resource_grants_lookup ON resource_grants(tenant_id, resource_type);
CREATE TABLE IF NOT EXISTS triggers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	agent_id TEXT,
	channel_id TEXT,
	chat_id TEXT,
	cron_expr TEXT,
	prompt TEXT,
	event TEXT,
	action_type TEXT,
	action_target TEXT,
	token TEXT,
	session_mode TEXT,
	enabled BIGINT DEFAULT 0,
	last_run TEXT,
	last_status TEXT,
	last_error TEXT,
	owner_tenant_id TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_triggers_owner ON triggers(owner_tenant_id);
CREATE TABLE IF NOT EXISTS conversations (
	id TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	conversation_key TEXT,
	chat_id TEXT NOT NULL,
	chat_type TEXT,
	agent_id TEXT,
	work_dir TEXT,
	native_session_id TEXT,
	title TEXT,
	message_count BIGINT DEFAULT 0,
	created_at TEXT,
	updated_at TEXT,
	last_active_at TEXT,
	ended_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_conversations_scope ON conversations(scope);
CREATE TABLE IF NOT EXISTS channel_tasks (
	id TEXT PRIMARY KEY,
	channel_id TEXT NOT NULL,
	conversation_id TEXT,
	conversation_key TEXT NOT NULL,
	chat_id TEXT,
	message_id TEXT,
	chat_type TEXT,
	root_id TEXT,
	thread_id TEXT,
	user_id TEXT,
	controller_id TEXT,
	native_thread_id TEXT,
	turn_id TEXT,
	status TEXT NOT NULL,
	error TEXT,
	delivery_key TEXT,
	delivery_status TEXT,
	delivery_attempts BIGINT DEFAULT 0,
	delivery_error TEXT,
	delivered_at TEXT,
	feedback_nonce TEXT,
	prompt TEXT,
	created_at TEXT,
	started_at TEXT,
	finished_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_channel_tasks_conversation
	ON channel_tasks(channel_id, conversation_key, created_at);
CREATE INDEX IF NOT EXISTS idx_channel_tasks_status
	ON channel_tasks(channel_id, status, created_at);
CREATE TABLE IF NOT EXISTS channel_interactions (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	channel_id TEXT NOT NULL,
	conversation_id TEXT,
	conversation_key TEXT NOT NULL,
	controller_id TEXT,
	nonce TEXT NOT NULL,
	message_id TEXT,
	status TEXT NOT NULL,
	request TEXT NOT NULL,
	created_at TEXT,
	expires_at TEXT,
	resolved_at TEXT,
	resolved_by TEXT
);
CREATE INDEX IF NOT EXISTS idx_channel_interactions_pending
	ON channel_interactions(channel_id, conversation_key, status, created_at);
CREATE TABLE IF NOT EXISTS channel_feedback (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	channel_id TEXT NOT NULL,
	conversation_id TEXT,
	user_id TEXT NOT NULL,
	semantic TEXT NOT NULL,
	reason TEXT,
	comment TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(task_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_channel_feedback_channel_updated
	ON channel_feedback(channel_id, updated_at);
CREATE TABLE IF NOT EXISTS orchestrations (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	status TEXT NOT NULL,
	max_concurrency BIGINT NOT NULL,
	error TEXT,
	owner_tenant_id TEXT,
	created_at TEXT NOT NULL,
	started_at TEXT,
	finished_at TEXT,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orchestrations_owner ON orchestrations(owner_tenant_id);
CREATE TABLE IF NOT EXISTS orchestration_tasks (
	orchestration_id TEXT NOT NULL,
	id TEXT NOT NULL,
	agent_id TEXT,
	project TEXT,
	input TEXT NOT NULL,
	depends_on TEXT,
	status TEXT NOT NULL,
	output TEXT,
	error TEXT,
	invocation_id TEXT,
	conversation_id TEXT,
	created_at TEXT NOT NULL,
	started_at TEXT,
	finished_at TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(orchestration_id,id)
);
CREATE INDEX IF NOT EXISTS idx_orchestrations_status_updated
	ON orchestrations(status,updated_at);
CREATE INDEX IF NOT EXISTS idx_orchestration_tasks_status
	ON orchestration_tasks(orchestration_id,status);

CREATE TABLE IF NOT EXISTS observation_traces (
	trace_id TEXT PRIMARY KEY,
	root_span_id TEXT,
	name TEXT,
	started_at TEXT NOT NULL,
	ended_at TEXT,
	agent_id TEXT,
	agent_name TEXT,
	runtime_id TEXT,
	conversation_id TEXT,
	session_id TEXT,
	turn_id TEXT,
	source TEXT,
	provenance TEXT,
	quality TEXT,
	status TEXT,
	error_json TEXT,
	model_json TEXT,
	input_tokens BIGINT DEFAULT 0,
	output_tokens BIGINT DEFAULT 0,
	cache_read_tokens BIGINT DEFAULT 0,
	cache_write_tokens BIGINT DEFAULT 0,
	reasoning_tokens BIGINT DEFAULT 0,
	tool_tokens BIGINT DEFAULT 0,
	total_tokens BIGINT DEFAULT 0,
	cost_usd DOUBLE PRECISION DEFAULT 0,
	span_count BIGINT DEFAULT 0,
	event_count BIGINT DEFAULT 0,
	attributes TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observation_traces_started ON observation_traces(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_traces_agent_started ON observation_traces(agent_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_traces_session_started ON observation_traces(session_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_traces_conversation_started ON observation_traces(conversation_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_traces_status_started ON observation_traces(status, started_at DESC);

CREATE TABLE IF NOT EXISTS observation_spans (
	span_id TEXT PRIMARY KEY,
	trace_id TEXT NOT NULL,
	parent_span_id TEXT,
	kind TEXT NOT NULL,
	name TEXT,
	sequence BIGINT DEFAULT 0,
	started_at TEXT NOT NULL,
	ended_at TEXT,
	duration_ms BIGINT DEFAULT 0,
	agent_id TEXT,
	runtime_id TEXT,
	conversation_id TEXT,
	session_id TEXT,
	turn_id TEXT,
	source TEXT,
	provenance TEXT,
	quality TEXT,
	status TEXT,
	error_json TEXT,
	model_json TEXT,
	tool_json TEXT,
	payload_id TEXT,
	input_tokens BIGINT DEFAULT 0,
	output_tokens BIGINT DEFAULT 0,
	cache_read_tokens BIGINT DEFAULT 0,
	cache_write_tokens BIGINT DEFAULT 0,
	reasoning_tokens BIGINT DEFAULT 0,
	tool_tokens BIGINT DEFAULT 0,
	total_tokens BIGINT DEFAULT 0,
	cost_usd DOUBLE PRECISION DEFAULT 0,
	attributes TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observation_spans_trace_sequence ON observation_spans(trace_id, sequence, started_at);
CREATE INDEX IF NOT EXISTS idx_observation_spans_trace_parent ON observation_spans(trace_id, parent_span_id);
CREATE INDEX IF NOT EXISTS idx_observation_spans_kind_started ON observation_spans(kind, started_at DESC);

CREATE TABLE IF NOT EXISTS observation_events (
	event_id TEXT PRIMARY KEY,
	dedupe_key TEXT,
	trace_id TEXT NOT NULL,
	span_id TEXT NOT NULL,
	parent_span_id TEXT,
	sequence BIGINT DEFAULT 0,
	timestamp TEXT NOT NULL,
	kind TEXT NOT NULL,
	name TEXT,
	lifecycle TEXT,
	source TEXT,
	quality TEXT,
	status TEXT,
	payload_id TEXT,
	envelope_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_observation_events_dedupe ON observation_events(dedupe_key) WHERE dedupe_key IS NOT NULL AND dedupe_key <> '';
CREATE INDEX IF NOT EXISTS idx_observation_events_trace_sequence ON observation_events(trace_id, sequence, timestamp);
CREATE INDEX IF NOT EXISTS idx_observation_events_span_time ON observation_events(span_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_observation_events_source_time ON observation_events(source, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_observation_events_payload ON observation_events(payload_id) WHERE payload_id <> '';

CREATE TABLE IF NOT EXISTS observation_data_keys (
	key_id TEXT PRIMARY KEY,
	wrap_nonce BYTEA NOT NULL,
	wrapped_key BYTEA NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS observation_payloads (
	payload_id TEXT PRIMARY KEY,
	key_id TEXT NOT NULL,
	content_type TEXT,
	compression TEXT NOT NULL,
	encryption TEXT NOT NULL,
	nonce BYTEA NOT NULL,
	ciphertext BYTEA NOT NULL,
	sha256 TEXT NOT NULL,
	original_bytes BIGINT DEFAULT 0,
	stored_bytes BIGINT DEFAULT 0,
	redacted BIGINT DEFAULT 0,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observation_payloads_expiry ON observation_payloads(expires_at);
CREATE INDEX IF NOT EXISTS idx_observation_payloads_key ON observation_payloads(key_id);

CREATE TABLE IF NOT EXISTS observation_payload_chunks (
	payload_id TEXT NOT NULL,
	chunk_index BIGINT NOT NULL,
	nonce BYTEA NOT NULL,
	ciphertext BYTEA NOT NULL,
	original_bytes BIGINT DEFAULT 0,
	stored_bytes BIGINT DEFAULT 0,
	PRIMARY KEY(payload_id, chunk_index)
);
CREATE INDEX IF NOT EXISTS idx_observation_payload_chunks_payload ON observation_payload_chunks(payload_id, chunk_index);

CREATE TABLE IF NOT EXISTS observation_daily_usage (
	day TEXT NOT NULL,
	agent_id TEXT NOT NULL DEFAULT '',
	runtime_id TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	input_tokens BIGINT DEFAULT 0,
	output_tokens BIGINT DEFAULT 0,
	cache_read_tokens BIGINT DEFAULT 0,
	cache_write_tokens BIGINT DEFAULT 0,
	cost_usd DOUBLE PRECISION DEFAULT 0,
	requests BIGINT DEFAULT 0,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(day,agent_id,runtime_id,model,source)
);
CREATE INDEX IF NOT EXISTS idx_observation_daily_usage_day ON observation_daily_usage(day DESC);

CREATE TABLE IF NOT EXISTS observation_ingest_cursors (
	source TEXT NOT NULL,
	resource TEXT NOT NULL,
	cursor TEXT,
	message_id TEXT,
	file_identity TEXT,
	byte_offset BIGINT DEFAULT 0,
	observed_at TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(source, resource)
);
CREATE INDEX IF NOT EXISTS idx_observation_ingest_cursors_updated ON observation_ingest_cursors(updated_at);

CREATE TABLE IF NOT EXISTS observation_export_outbox (
	id TEXT PRIMARY KEY,
	exporter TEXT NOT NULL,
	event_id TEXT NOT NULL,
	trace_id TEXT NOT NULL,
	envelope_json TEXT NOT NULL,
	include_content BIGINT DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'pending',
	attempts BIGINT DEFAULT 0,
	next_attempt_at TEXT,
	last_error TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(exporter, event_id)
);
CREATE INDEX IF NOT EXISTS idx_observation_outbox_ready ON observation_export_outbox(exporter, status, next_attempt_at, created_at);

CREATE TABLE IF NOT EXISTS observation_insights (
	id TEXT PRIMARY KEY,
	rule_id TEXT NOT NULL,
	agent_id TEXT,
	trace_id TEXT,
	severity TEXT,
	status TEXT NOT NULL DEFAULT 'open',
	title TEXT NOT NULL,
	summary TEXT,
	suggestion TEXT,
	sample_size BIGINT DEFAULT 0,
	confidence DOUBLE PRECISION DEFAULT 0,
	estimated_token_savings BIGINT DEFAULT 0,
	estimated_cost_savings_usd DOUBLE PRECISION DEFAULT 0,
	related_trace_ids TEXT,
	only_suggestion BIGINT DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observation_insights_status_created ON observation_insights(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_insights_agent_created ON observation_insights(agent_id, created_at DESC);

CREATE TABLE IF NOT EXISTS observation_integration_ownership (
	install_id TEXT NOT NULL,
	host TEXT NOT NULL,
	scope TEXT NOT NULL,
	resource_key TEXT NOT NULL,
	version TEXT,
	sha256 TEXT,
	handler_fingerprint TEXT,
	target_path TEXT,
	before_hash TEXT,
	after_hash TEXT,
	metadata TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(install_id, resource_key)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_observation_ownership_resource ON observation_integration_ownership(host, scope, resource_key);

CREATE TABLE IF NOT EXISTS observation_resource_leases (
	resource_key TEXT PRIMARY KEY,
	owner_id TEXT NOT NULL,
	install_id TEXT,
	lease_token TEXT NOT NULL,
	acquired_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	metadata TEXT
);
CREATE INDEX IF NOT EXISTS idx_observation_resource_leases_expiry ON observation_resource_leases(expires_at);
