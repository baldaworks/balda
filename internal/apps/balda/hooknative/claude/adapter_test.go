package claude_test

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hooknative/claude"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativeadaptertest"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativetest"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

func TestAdapterContract(t *testing.T) {
	hooknativetest.RunAdapterContract(t, hooknativeadaptertest.RealAdapterFactory(claude.New, claude.Protocol{}, agentconfig.AgentTypeClaudeCodeACP, false))
}
