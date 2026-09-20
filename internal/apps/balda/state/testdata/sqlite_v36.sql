-- Current SQLite schema at migration 36, captured before backend selection.
-- Keep this fixture independent of the migration code under test.

CREATE TABLE "balda_app_kv" (
    namespace TEXT NOT NULL,
    key TEXT NOT NULL,
    value_json TEXT NOT NULL,
    updated_at TEXT NOT NULL, expires_at TEXT,
    PRIMARY KEY (namespace, key)
);

CREATE TABLE "balda_collaborators" (
    user_id TEXT PRIMARY KEY,
    username TEXT NOT NULL DEFAULT '',
    first_name TEXT NOT NULL DEFAULT '',
    added_by TEXT NOT NULL,
    added_at TEXT NOT NULL
);

CREATE TABLE balda_plugin_activation_intents (
			intent_id TEXT PRIMARY KEY,
			plugin_id TEXT NOT NULL,
			from_revision_id TEXT,
			to_revision_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('pending', 'complete')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);

CREATE TABLE balda_plugin_installs (
			plugin_id TEXT PRIMARY KEY,
			origin_marketplace TEXT NOT NULL,
			origin_source TEXT NOT NULL,
			origin_path TEXT NOT NULL,
			active_revision_id TEXT NOT NULL,
			enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
			version TEXT NOT NULL DEFAULT '',
			capability_json TEXT NOT NULL,
			data_relative_path TEXT NOT NULL,
			updated_at TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (plugin_id, active_revision_id)
				REFERENCES balda_plugin_revisions(plugin_id, revision_id) ON DELETE RESTRICT
		);

CREATE TABLE balda_plugin_revisions (
			plugin_id TEXT NOT NULL,
			revision_id TEXT NOT NULL,
			version TEXT NOT NULL DEFAULT '',
			relative_root TEXT NOT NULL,
			capability_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			retired_at TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (plugin_id, revision_id)
		);

CREATE TABLE balda_questions (
			question_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			channel_kind TEXT NOT NULL,
			address_key TEXT NOT NULL,
			address_json TEXT NOT NULL,
			prompt TEXT NOT NULL,
			status TEXT NOT NULL,
			interaction_json TEXT NOT NULL,
			resume_json TEXT NOT NULL,
			request_json TEXT NOT NULL,
			answer_json TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			conversation_key TEXT NOT NULL DEFAULT '',
			provider_message_id TEXT NOT NULL DEFAULT '',
			reply_handle TEXT NOT NULL DEFAULT '',
			control_handle TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL DEFAULT '',
			answered_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		, failure_json TEXT NOT NULL DEFAULT '', failed_at TEXT NOT NULL DEFAULT '');

CREATE TABLE "balda_runtime_app_state" (
    app_name TEXT PRIMARY KEY,
    state_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE "balda_runtime_events" (
    app_name TEXT NOT NULL,
    user_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    timestamp TEXT NOT NULL,
    event_json TEXT NOT NULL,
    PRIMARY KEY (app_name, user_id, session_id, event_id),
    FOREIGN KEY (app_name, user_id, session_id)
        REFERENCES "balda_runtime_sessions"(app_name, user_id, session_id)
        ON DELETE CASCADE
);

CREATE TABLE "balda_runtime_sessions" (
    app_name TEXT NOT NULL,
    user_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    state_json TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (app_name, user_id, session_id)
);

CREATE TABLE "balda_runtime_user_state" (
    app_name TEXT NOT NULL,
    user_id TEXT NOT NULL,
    state_json TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (app_name, user_id)
);

CREATE TABLE "balda_scheduled_jobs" (
    job_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    channel_type TEXT NOT NULL,
    address_key TEXT NOT NULL,
    address_json TEXT NOT NULL,
    content TEXT NOT NULL,
    schedule_spec TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    status TEXT NOT NULL DEFAULT 'active',
    max_retries INTEGER NOT NULL DEFAULT 3,
    retry_count INTEGER NOT NULL DEFAULT 0,
    last_dispatch_key TEXT NOT NULL DEFAULT '',
    next_run_at TEXT NOT NULL,
    last_run_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
, report_to_enabled INTEGER NOT NULL DEFAULT 0, report_to_session_id TEXT NOT NULL DEFAULT '', report_to_channel_type TEXT NOT NULL DEFAULT '', report_to_address_key TEXT NOT NULL DEFAULT '', report_to_address_json TEXT NOT NULL DEFAULT '');

CREATE TABLE "balda_session_metadata" (
    session_id TEXT PRIMARY KEY,
    chat_id INTEGER NOT NULL DEFAULT 0,
    topic_id INTEGER NOT NULL DEFAULT 0,
    agent_name TEXT NOT NULL,
    workspace_dir TEXT NOT NULL,
    branch_name TEXT NOT NULL,
    status TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    channel_type TEXT NOT NULL DEFAULT 'telegram',
    address_key TEXT NOT NULL DEFAULT '',
    address_json TEXT NOT NULL DEFAULT '{}',
    user_id TEXT NOT NULL DEFAULT ''
, runtime_snapshot_id TEXT NOT NULL DEFAULT '');

CREATE TABLE "balda_telegram_offsets" (
    bot_key TEXT PRIMARY KEY,
    offset INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE "execution_agent_steps" (
    id TEXT PRIMARY KEY,
    step_key TEXT NOT NULL UNIQUE,
    job_id TEXT NOT NULL,
    agent_name TEXT NOT NULL,
    role TEXT NOT NULL,
    iteration INTEGER NOT NULL,
    payload_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    result_json TEXT,
    error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
, result TEXT);

CREATE TABLE "execution_delivery_outbox" (
    id TEXT PRIMARY KEY,
    delivery_key TEXT NOT NULL UNIQUE,
    job_id TEXT,
    session_id TEXT,
    channel TEXT NOT NULL,
    address_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    provider_message_id TEXT,
    sent_at TEXT,
    error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
, payload TEXT);

CREATE TABLE execution_job_event_outbox (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			subject TEXT NOT NULL,
			envelope_json TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT,
			created_at TEXT NOT NULL,
			published_at TEXT
		, envelope TEXT);

CREATE TABLE "execution_job_events" (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    actor TEXT,
    message_id TEXT,
    payload_json TEXT,
    created_at TEXT NOT NULL
, payload TEXT);

CREATE TABLE "execution_jobs" (
    id TEXT PRIMARY KEY,
    session_id TEXT,
    parent_job_id TEXT,

    title TEXT,
    objective TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'queued',
    owner_actor TEXT,
    assigned_actor TEXT,

    priority INTEGER NOT NULL DEFAULT 0,

    created_by TEXT,
    created_from TEXT,

    plan_json TEXT,
    result_json TEXT,
    error TEXT,

    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    canceled_at TEXT
, result TEXT);

CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);

CREATE TABLE session_memory_ingress_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    export_id TEXT NOT NULL,
    action TEXT NOT NULL,
    actor TEXT NOT NULL,
    reason TEXT NOT NULL,
    occurred_at TEXT NOT NULL
);

CREATE TABLE session_memory_ingress_outbox (
    export_id TEXT PRIMARY KEY,
    scope_key TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    scope_sequence INTEGER NOT NULL,
    subject TEXT NOT NULL,
    envelope_json TEXT NOT NULL,
    state TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    published_at TEXT NOT NULL DEFAULT '', next_attempt_at TEXT NOT NULL DEFAULT '',
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

INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (2, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (3, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (4, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (5, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (6, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (7, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (8, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (9, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (10, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (11, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (12, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (13, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (14, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (15, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (16, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (17, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (18, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (19, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (20, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (21, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (22, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (23, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (24, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (25, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (26, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (27, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (28, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (29, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (30, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (31, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (32, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (33, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (34, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (35, 1);
INSERT INTO goose_db_version (version_id, is_applied) VALUES (36, 1);
