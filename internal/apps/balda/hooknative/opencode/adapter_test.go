package opencode_test

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hooknative/opencode"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativeadaptertest"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativetest"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

func TestAdapterContract(t *testing.T) {
	hooknativetest.RunAdapterContract(t, hooknativeadaptertest.RealAdapterFactory(opencode.New, opencode.Protocol{}, agentconfig.AgentTypeOpenCodeACP, true))
}
