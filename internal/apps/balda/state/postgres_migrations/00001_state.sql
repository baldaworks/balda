-- PostgreSQL baseline matching the supported SQLite v36 logical schema.
-- +goose Up
CREATE TABLE "balda_app_kv" (
    namespace TEXT COLLATE "C" NOT NULL,
    key TEXT COLLATE "C" NOT NULL,
    value_json TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL, expires_at TEXT COLLATE "C",
    PRIMARY KEY (namespace, key)
);

CREATE TABLE "balda_collaborators" (
    user_id TEXT COLLATE "C" PRIMARY KEY,
    username TEXT COLLATE "C" NOT NULL DEFAULT '',
    first_name TEXT COLLATE "C" NOT NULL DEFAULT '',
    added_by TEXT COLLATE "C" NOT NULL,
    added_at TEXT COLLATE "C" NOT NULL
);

CREATE TABLE balda_plugin_activation_intents (
			intent_id TEXT COLLATE "C" PRIMARY KEY,
			plugin_id TEXT COLLATE "C" NOT NULL,
			from_revision_id TEXT COLLATE "C",
			to_revision_id TEXT COLLATE "C" NOT NULL,
			operation TEXT COLLATE "C" NOT NULL,
			state TEXT COLLATE "C" NOT NULL CHECK (state IN ('pending', 'complete')),
			created_at TEXT COLLATE "C" NOT NULL,
			updated_at TEXT COLLATE "C" NOT NULL
		);

CREATE TABLE balda_plugin_revisions (
			plugin_id TEXT COLLATE "C" NOT NULL,
			revision_id TEXT COLLATE "C" NOT NULL,
			version TEXT COLLATE "C" NOT NULL DEFAULT '',
			relative_root TEXT COLLATE "C" NOT NULL,
			capability_json TEXT COLLATE "C" NOT NULL,
			created_at TEXT COLLATE "C" NOT NULL,
			retired_at TEXT COLLATE "C" NOT NULL DEFAULT '', description TEXT COLLATE "C" NOT NULL DEFAULT '',
			PRIMARY KEY (plugin_id, revision_id)
		);

CREATE TABLE balda_plugin_installs (
			plugin_id TEXT COLLATE "C" PRIMARY KEY,
			origin_marketplace TEXT COLLATE "C" NOT NULL,
			origin_source TEXT COLLATE "C" NOT NULL,
			origin_path TEXT COLLATE "C" NOT NULL,
			active_revision_id TEXT COLLATE "C" NOT NULL,
			enabled BIGINT NOT NULL CHECK (enabled IN (0, 1)),
			version TEXT COLLATE "C" NOT NULL DEFAULT '',
			capability_json TEXT COLLATE "C" NOT NULL,
			data_relative_path TEXT COLLATE "C" NOT NULL,
			updated_at TEXT COLLATE "C" NOT NULL, description TEXT COLLATE "C" NOT NULL DEFAULT '',
			FOREIGN KEY (plugin_id, active_revision_id)
				REFERENCES balda_plugin_revisions(plugin_id, revision_id) ON DELETE RESTRICT
		);

CREATE TABLE balda_questions (
			question_id TEXT COLLATE "C" PRIMARY KEY,
			session_id TEXT COLLATE "C" NOT NULL,
			channel_kind TEXT COLLATE "C" NOT NULL,
			address_key TEXT COLLATE "C" NOT NULL,
			address_json TEXT COLLATE "C" NOT NULL,
			prompt TEXT COLLATE "C" NOT NULL,
			status TEXT COLLATE "C" NOT NULL,
			interaction_json TEXT COLLATE "C" NOT NULL,
			resume_json TEXT COLLATE "C" NOT NULL,
			request_json TEXT COLLATE "C" NOT NULL,
			answer_json TEXT COLLATE "C" NOT NULL DEFAULT '',
			provider TEXT COLLATE "C" NOT NULL DEFAULT '',
			conversation_key TEXT COLLATE "C" NOT NULL DEFAULT '',
			provider_message_id TEXT COLLATE "C" NOT NULL DEFAULT '',
			reply_handle TEXT COLLATE "C" NOT NULL DEFAULT '',
			control_handle TEXT COLLATE "C" NOT NULL DEFAULT '',
			expires_at TEXT COLLATE "C" NOT NULL DEFAULT '',
			answered_at TEXT COLLATE "C" NOT NULL DEFAULT '',
			created_at TEXT COLLATE "C" NOT NULL,
			updated_at TEXT COLLATE "C" NOT NULL
		, failure_json TEXT COLLATE "C" NOT NULL DEFAULT '', failed_at TEXT COLLATE "C" NOT NULL DEFAULT '');

CREATE TABLE "balda_runtime_app_state" (
    app_name TEXT COLLATE "C" PRIMARY KEY,
    state_json TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL
);

CREATE TABLE "balda_runtime_sessions" (
    app_name TEXT COLLATE "C" NOT NULL,
    user_id TEXT COLLATE "C" NOT NULL,
    session_id TEXT COLLATE "C" NOT NULL,
    state_json TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (app_name, user_id, session_id)
);

CREATE TABLE "balda_runtime_events" (
    app_name TEXT COLLATE "C" NOT NULL,
    user_id TEXT COLLATE "C" NOT NULL,
    session_id TEXT COLLATE "C" NOT NULL,
    event_id TEXT COLLATE "C" NOT NULL,
    ordinal BIGINT NOT NULL,
    timestamp TEXT COLLATE "C" NOT NULL,
    event_json TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (app_name, user_id, session_id, event_id),
    FOREIGN KEY (app_name, user_id, session_id)
        REFERENCES "balda_runtime_sessions"(app_name, user_id, session_id)
        ON DELETE CASCADE
);

CREATE TABLE "balda_runtime_user_state" (
    app_name TEXT COLLATE "C" NOT NULL,
    user_id TEXT COLLATE "C" NOT NULL,
    state_json TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (app_name, user_id)
);

CREATE TABLE "balda_scheduled_jobs" (
    job_id TEXT COLLATE "C" PRIMARY KEY,
    session_id TEXT COLLATE "C" NOT NULL,
    channel_type TEXT COLLATE "C" NOT NULL,
    address_key TEXT COLLATE "C" NOT NULL,
    address_json TEXT COLLATE "C" NOT NULL,
    content TEXT COLLATE "C" NOT NULL,
    schedule_spec TEXT COLLATE "C" NOT NULL,
    timezone TEXT COLLATE "C" NOT NULL DEFAULT 'UTC',
    status TEXT COLLATE "C" NOT NULL DEFAULT 'active',
    max_retries BIGINT NOT NULL DEFAULT 3,
    retry_count BIGINT NOT NULL DEFAULT 0,
    last_dispatch_key TEXT COLLATE "C" NOT NULL DEFAULT '',
    next_run_at TEXT COLLATE "C" NOT NULL,
    last_run_at TEXT COLLATE "C" NOT NULL DEFAULT '',
    last_error TEXT COLLATE "C" NOT NULL DEFAULT '',
    created_at TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL
, report_to_enabled BIGINT NOT NULL DEFAULT 0, report_to_session_id TEXT COLLATE "C" NOT NULL DEFAULT '', report_to_channel_type TEXT COLLATE "C" NOT NULL DEFAULT '', report_to_address_key TEXT COLLATE "C" NOT NULL DEFAULT '', report_to_address_json TEXT COLLATE "C" NOT NULL DEFAULT '');

CREATE TABLE "balda_session_metadata" (
    session_id TEXT COLLATE "C" PRIMARY KEY,
    chat_id BIGINT NOT NULL DEFAULT 0,
    topic_id BIGINT NOT NULL DEFAULT 0,
    agent_name TEXT COLLATE "C" NOT NULL,
    workspace_dir TEXT COLLATE "C" NOT NULL,
    branch_name TEXT COLLATE "C" NOT NULL,
    status TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL,
    channel_type TEXT COLLATE "C" NOT NULL DEFAULT 'telegram',
    address_key TEXT COLLATE "C" NOT NULL DEFAULT '',
    address_json TEXT COLLATE "C" NOT NULL DEFAULT '{}',
    user_id TEXT COLLATE "C" NOT NULL DEFAULT ''
, runtime_snapshot_id TEXT COLLATE "C" NOT NULL DEFAULT '');

CREATE TABLE "balda_telegram_offsets" (
    bot_key TEXT COLLATE "C" PRIMARY KEY,
    "offset" BIGINT NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL
);

CREATE TABLE "execution_agent_steps" (
    id TEXT COLLATE "C" PRIMARY KEY,
    step_key TEXT COLLATE "C" NOT NULL UNIQUE,
    job_id TEXT COLLATE "C" NOT NULL,
    agent_name TEXT COLLATE "C" NOT NULL,
    role TEXT COLLATE "C" NOT NULL,
    iteration BIGINT NOT NULL,
    payload_hash TEXT COLLATE "C" NOT NULL,
    status TEXT COLLATE "C" NOT NULL,
    result_json TEXT COLLATE "C",
    error TEXT COLLATE "C",
    created_at TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL,
    completed_at TEXT COLLATE "C"
, result TEXT COLLATE "C");

CREATE TABLE "execution_delivery_outbox" (
    id TEXT COLLATE "C" PRIMARY KEY,
    delivery_key TEXT COLLATE "C" NOT NULL UNIQUE,
    job_id TEXT COLLATE "C",
    session_id TEXT COLLATE "C",
    channel TEXT COLLATE "C" NOT NULL,
    address_key TEXT COLLATE "C" NOT NULL,
    kind TEXT COLLATE "C" NOT NULL,
    payload_json TEXT COLLATE "C" NOT NULL,
    payload_hash TEXT COLLATE "C" NOT NULL,
    status TEXT COLLATE "C" NOT NULL,
    provider_message_id TEXT COLLATE "C",
    sent_at TEXT COLLATE "C",
    error TEXT COLLATE "C",
    created_at TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL
, payload TEXT COLLATE "C");

CREATE TABLE execution_job_event_outbox (
			id TEXT COLLATE "C" PRIMARY KEY,
			job_id TEXT COLLATE "C" NOT NULL,
			subject TEXT COLLATE "C" NOT NULL,
			envelope_json TEXT COLLATE "C" NOT NULL,
			attempts BIGINT NOT NULL DEFAULT 0,
			last_error TEXT COLLATE "C",
			created_at TEXT COLLATE "C" NOT NULL,
			published_at TEXT COLLATE "C"
		, envelope TEXT COLLATE "C");

CREATE TABLE "execution_job_events" (
    id TEXT COLLATE "C" PRIMARY KEY,
    job_id TEXT COLLATE "C" NOT NULL,
    event_type TEXT COLLATE "C" NOT NULL,
    actor TEXT COLLATE "C",
    message_id TEXT COLLATE "C",
    payload_json TEXT COLLATE "C",
    created_at TEXT COLLATE "C" NOT NULL
, payload TEXT COLLATE "C");

CREATE TABLE "execution_jobs" (
    id TEXT COLLATE "C" PRIMARY KEY,
    session_id TEXT COLLATE "C",
    parent_job_id TEXT COLLATE "C",

    title TEXT COLLATE "C",
    objective TEXT COLLATE "C" NOT NULL,

    status TEXT COLLATE "C" NOT NULL DEFAULT 'queued',
    owner_actor TEXT COLLATE "C",
    assigned_actor TEXT COLLATE "C",

    priority BIGINT NOT NULL DEFAULT 0,

    created_by TEXT COLLATE "C",
    created_from TEXT COLLATE "C",

    plan_json TEXT COLLATE "C",
    result_json TEXT COLLATE "C",
    error TEXT COLLATE "C",

    created_at TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL,
    started_at TEXT COLLATE "C",
    completed_at TEXT COLLATE "C",
    canceled_at TEXT COLLATE "C"
, result TEXT COLLATE "C");

CREATE TABLE session_memory_ingress_audit (
    id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    export_id TEXT COLLATE "C" NOT NULL,
    action TEXT COLLATE "C" NOT NULL,
    actor TEXT COLLATE "C" NOT NULL,
    reason TEXT COLLATE "C" NOT NULL,
    occurred_at TEXT COLLATE "C" NOT NULL
);

CREATE TABLE session_memory_ingress_outbox (
    export_id TEXT COLLATE "C" PRIMARY KEY,
    scope_key TEXT COLLATE "C" NOT NULL,
    scope_kind TEXT COLLATE "C" NOT NULL,
    scope_sequence BIGINT NOT NULL,
    subject TEXT COLLATE "C" NOT NULL,
    envelope_json TEXT COLLATE "C" NOT NULL,
    state TEXT COLLATE "C" NOT NULL,
    attempts BIGINT NOT NULL DEFAULT 0,
    lease_owner TEXT COLLATE "C" NOT NULL DEFAULT '',
    lease_until TEXT COLLATE "C" NOT NULL DEFAULT '',
    last_error TEXT COLLATE "C" NOT NULL DEFAULT '',
    created_at TEXT COLLATE "C" NOT NULL,
    updated_at TEXT COLLATE "C" NOT NULL,
    published_at TEXT COLLATE "C" NOT NULL DEFAULT '', next_attempt_at TEXT COLLATE "C" NOT NULL DEFAULT '',
    UNIQUE (scope_key, scope_sequence)
);

CREATE INDEX idx_balda_plugin_intents_state_created
			ON balda_plugin_activation_intents(state, created_at);

CREATE INDEX idx_balda_questions_reply_lookup
			ON balda_questions(provider, conversation_key, provider_message_id);

CREATE INDEX idx_balda_questions_session_status
			ON balda_questions(session_id, status, created_at);

CREATE INDEX idx_balda_questions_status_created
			ON balda_questions(status, created_at);

CREATE INDEX idx_balda_runtime_events_session_order ON balda_runtime_events(app_name, user_id, session_id, timestamp, ordinal);

CREATE INDEX idx_balda_runtime_sessions_app_user ON balda_runtime_sessions(app_name, user_id);

CREATE INDEX idx_balda_scheduled_jobs_due ON balda_scheduled_jobs(status, next_run_at);

CREATE INDEX idx_balda_scheduled_jobs_locator ON balda_scheduled_jobs(channel_type, address_key);

CREATE UNIQUE INDEX idx_balda_session_metadata_channel_address
    ON balda_session_metadata(channel_type, address_key);

CREATE INDEX idx_balda_session_metadata_status
    ON balda_session_metadata(status);

CREATE INDEX idx_execution_agent_steps_job ON execution_agent_steps(job_id, created_at);

CREATE INDEX idx_execution_delivery_outbox_job ON execution_delivery_outbox(job_id, created_at);

CREATE INDEX idx_execution_job_event_outbox_pending
			ON execution_job_event_outbox(published_at, created_at);

CREATE INDEX idx_execution_job_events_job ON execution_job_events(job_id, created_at);

CREATE INDEX idx_execution_jobs_session_status ON execution_jobs(session_id, status, updated_at);

CREATE INDEX idx_execution_jobs_status_updated ON execution_jobs(status, updated_at);

CREATE INDEX idx_session_memory_ingress_audit_export
    ON session_memory_ingress_audit(export_id, id);

CREATE INDEX idx_session_memory_ingress_claim
    ON session_memory_ingress_outbox(state, lease_until, scope_key, scope_sequence);

CREATE INDEX idx_session_memory_ingress_retry
    ON session_memory_ingress_outbox(state, next_attempt_at, scope_key, scope_sequence);
