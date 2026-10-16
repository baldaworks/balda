package codex_test

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hooknative/codex"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativeadaptertest"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativetest"
	"github.com/normahq/runtime/v2/agentconfig"
)

func TestAdapterContract(t *testing.T) {
	hooknativetest.RunAdapterContract(t, hooknativeadaptertest.RealAdapterFactory(codex.New, codex.Protocol{}, agentconfig.AgentTypeCodexACP, false))
}
