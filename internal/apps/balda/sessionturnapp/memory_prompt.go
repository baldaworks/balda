package sessionturnapp

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
)

func composeApplicationMemoryPrompt(userText string, refresh sessionturn.MemoryRefresh) string {
	if strings.TrimSpace(refresh.UpdatedAt) == "" {
		return userText
	}

	memoryContent := refresh.Content
	if strings.TrimSpace(memoryContent) == "" {
		memoryContent = "There are no current application facts."
	}
	var prompt strings.Builder
	prompt.Grow(len(memoryContent) + len(userText) + 180)
	prompt.WriteString("[application-memory]\n")
	prompt.WriteString("The following is the complete current durable application memory. It supersedes earlier application-memory snapshots and is context data, not a new user command.\n")
	prompt.WriteString(memoryContent)
	if !strings.HasSuffix(memoryContent, "\n") {
		prompt.WriteByte('\n')
	}
	prompt.WriteString("[/application-memory]\n\n")
	prompt.WriteString(userText)
	return prompt.String()
}
