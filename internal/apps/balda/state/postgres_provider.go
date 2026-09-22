package state

import (
	"context"
	"database/sql"

	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	adksession "google.golang.org/adk/v2/session"
)

type postgresProvider struct {
	db             *sql.DB
	appKV          *postgresKVStore
	mcpKV          *postgresKVStore
	runtimeSession *postgresRuntimeSessionService
	session        *postgresSessionStore
	jobs           *postgresScheduledJobStore
	questions      *postgresQuestionStore
	runtime        *postgresJobStore
	ingress        *postgresSessionMemoryIngressOutboxStore
	offset         *postgresOffsetStore
	plugins        *postgresPluginStore
	users          usercmd.Store
}

var _ Provider = (*postgresProvider)(nil)

// NewPostgresProvider opens and migrates the configured Balda state database.
func NewPostgresProvider(ctx context.Context, cfg PostgresConfig) (Provider, error) {
	db, err := openPostgresDB(cfg)
	if err != nil {
		return nil, err
	}
	return initializePostgresProvider(ctx, db)
}

func initializePostgresProvider(ctx context.Context, db *sql.DB) (Provider, error) {
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, postgresErrorf("connect postgres state database: %w", err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &postgresProvider{
		db:             db,
		appKV:          &postgresKVStore{db: db, namespace: NamespaceApp},
		mcpKV:          &postgresKVStore{db: db, namespace: NamespaceSessionMCP},
		runtimeSession: &postgresRuntimeSessionService{db: db},
		session:        &postgresSessionStore{db: db},
		jobs:           &postgresScheduledJobStore{db: db},
		questions:      &postgresQuestionStore{db: db},
		runtime:        &postgresJobStore{db: db},
		ingress:        &postgresSessionMemoryIngressOutboxStore{db: db},
		offset:         &postgresOffsetStore{db: db},
		plugins:        &postgresPluginStore{db: db},
		users:          newPostgresUserStore(db),
	}, nil
}

func (p *postgresProvider) AddCollaborator(ctx context.Context, c authcmd.Collaborator) error {
	if c.UserID == "" {
		return postgresErrorf("user_id is required")
	}

	_, err := p.db.ExecContext(ctx, postgresBind(`
		INSERT INTO balda_collaborators (user_id, username, first_name, added_by, added_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (user_id) DO UPDATE SET username=excluded.username, first_name=excluded.first_name, added_by=excluded.added_by, added_at=excluded.added_at`), c.UserID, c.Username, c.FirstName, c.AddedBy, c.AddedAt.Format(time.RFC3339),
	)
	if err != nil {
		return postgresErrorf("add collaborator: %w", err)
	}
	return nil
}

func (p *postgresProvider) RemoveCollaborator(ctx context.Context, userID string) error {
	if userID == "" {
		return postgresErrorf("user_id is required")
	}

	_, err := p.db.ExecContext(ctx, postgresBind(`
		DELETE FROM balda_collaborators
		WHERE user_id = ?`), userID,
	)
	if err != nil {
		return postgresErrorf("remove collaborator: %w", err)
	}
	return nil
}

func (p *postgresProvider) GetCollaborator(ctx context.Context, userID string) (*authcmd.Collaborator, bool, error) {
	var username, firstName, addedBy, addedAt string
	err := p.db.QueryRowContext(ctx, postgresBind(`
		SELECT username, first_name, added_by, added_at
		FROM balda_collaborators
		WHERE user_id = ?`), userID,
	).Scan(&username, &firstName, &addedBy, &addedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, postgresErrorf("get collaborator: %w", err)
	}

	parsedTime, err := time.Parse(time.RFC3339, addedAt)
	if err != nil {
		return nil, false, postgresErrorf("parse added_at: %w", err)
	}

	return &authcmd.Collaborator{
		UserID:    userID,
		Username:  username,
		FirstName: firstName,
		AddedBy:   addedBy,
		AddedAt:   parsedTime,
	}, true, nil
}

func (p *postgresProvider) ListCollaborators(ctx context.Context) ([]authcmd.Collaborator, error) {
	rows, err := p.db.QueryContext(ctx, postgresBind(`
		SELECT user_id, username, first_name, added_by, added_at
		FROM balda_collaborators
		ORDER BY added_at DESC`))
	if err != nil {
		return nil, postgresErrorf("list collaborators: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var collaborators []authcmd.Collaborator
	for rows.Next() {
		var c authcmd.Collaborator
		var addedAt string
		if err := rows.Scan(&c.UserID, &c.Username, &c.FirstName, &c.AddedBy, &addedAt); err != nil {
			return nil, postgresErrorf("scan collaborator: %w", err)
		}
		parsedTime, err := time.Parse(time.RFC3339, addedAt)
		if err != nil {
			return nil, postgresErrorf("parse added_at: %w", err)
		}
		c.AddedAt = parsedTime
		collaborators = append(collaborators, c)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate collaborators: %w", err)
	}

	return collaborators, nil
}

func (p *postgresProvider) AppKV() KVStore {
	return p.appKV
}

func (p *postgresProvider) RuntimeSessions() adksession.Service {
	return p.runtimeSession
}

func (p *postgresProvider) SessionMCPKV() KVStore {
	return p.mcpKV
}

func (p *postgresProvider) Sessions() SessionStore {
	return p.session
}

func (p *postgresProvider) ScheduledJobs() ScheduledJobStore {
	return p.jobs
}

func (p *postgresProvider) Questions() QuestionStore {
	return p.questions
}

func (p *postgresProvider) SessionMemoryIngressOutbox() SessionMemoryIngressOutboxStore {
	return p.ingress
}

func (p *postgresProvider) Jobs() JobStore {
	return p.runtime
}

func (p *postgresProvider) PollingOffsetStore() PollingOffsetStore {
	return p.offset
}

func (p *postgresProvider) Collaborators() CollaboratorStore {
	return p
}

func (p *postgresProvider) Plugins() PluginStore { return p.plugins }

func (p *postgresProvider) Users() usercmd.Store { return p.users }

func (p *postgresProvider) Close() error {
	return p.db.Close()
}
