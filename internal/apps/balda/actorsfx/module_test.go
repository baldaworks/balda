package actorsfx_test

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/actorsfx"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/controlapp"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryworkflow"
	"github.com/baldaworks/balda/internal/apps/balda/jobexec"
	baldajobs "github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/memory"
	"github.com/baldaworks/balda/internal/apps/balda/permissions"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/baldaworks/go-actorlayer/dispatch"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

type dummyDispatcher struct{}

func (d dummyDispatcher) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	return nil, nil
}

type dummyTurnRunner struct{}

func (r dummyTurnRunner) RunSessionTurnPayload(ctx context.Context, payload turncmd.SessionTurnPayload) error {
	return nil
}

func TestActorsfxModule_ProvidesAllProductActors(t *testing.T) {
	var actorsList []dispatch.Actor
	var queue appports.TurnQueue
	var turnDisp *actors.TurnDispatcher
	var registry *actors.JobRunRegistry
	var runs controlapp.JobRuns

	type params struct {
		fx.In
		Actors     []dispatch.Actor `group:"balda_product_actors"`
		Queue      appports.TurnQueue
		TurnDisp   *actors.TurnDispatcher
		Registry   *actors.JobRunRegistry
		Runs       controlapp.JobRuns
	}

	app := fx.New(
		fx.Provide(
			zerolog.Nop,
			func() *controlapp.Service {
				return controlapp.New(nil, nil, nil, nil, nil, zerolog.Nop())
			},
			func() *permissions.Service {
				return nil
			},
			func() *jobexec.Service {
				return jobexec.New(nil, nil)
			},
			func() *deliveryworkflow.Service {
				return deliveryworkflow.New(nil, nil, nil, nil, nil, zerolog.Nop())
			},
			func() actortransport.Dispatcher {
				return dummyDispatcher{}
			},
			func() appports.SessionTurnRunner {
				return dummyTurnRunner{}
			},
			func() *memory.Store {
				return nil
			},
			func() *baldaagent.RuntimeManager {
				return nil
			},
			func() *baldajobs.JobLifecycleService {
				return nil
			},
			func() *baldajobs.JobEventsService {
				return nil
			},
			func() *baldasession.Manager {
				return nil
			},
			func() *questions.Service {
				return nil
			},
			fx.Annotate(
				func() int { return 10 },
				fx.ResultTags(`name:"balda_goal_max_iterations"`),
			),
		),
		actorsfx.Module,
		fx.Invoke(func(p params) {
			actorsList = p.Actors
			queue = p.Queue
			turnDisp = p.TurnDisp
			registry = p.Registry
			runs = p.Runs
		}),
	)

	require.NoError(t, app.Err())
	require.NotNil(t, queue)
	require.NotNil(t, turnDisp)
	require.NotNil(t, registry)
	require.NotNil(t, runs)
	require.Len(t, actorsList, 8)

	addresses := make(map[string]bool)
	for _, a := range actorsList {
		addresses[a.Address()] = true
	}

	require.True(t, addresses["system:control"])
	require.True(t, addresses["permission:*"])
	require.True(t, addresses["session:*"])
	require.True(t, addresses["job:*"])
	require.True(t, addresses["goalkeeper:*"])
	require.True(t, addresses["memory:*"])
	require.True(t, addresses["question:*"])
	require.True(t, addresses["delivery:*"])
}
