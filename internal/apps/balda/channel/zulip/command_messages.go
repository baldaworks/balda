package zulip

import "fmt"

func CancelUsageMessage() string { return zulipCancelUsageText }
func CancelUnavailableMessage() string { return "Cancel is unavailable right now. Please try again." }
func CancelFailedMessage() string { return "Could not request cancel." }
func CancelRequestedMessage() string { return "Cancel requested." }

func LocatorUsageMessage() string { return zulipLocatorUsageText }

func UsageUsageMessage() string { return "Usage: /usage" }
func UsageEmptyMessage() string { return "No provider usage has been recorded for this session yet." }

func CloseDirectMessageOnly() string { return zulipDirectMessageOnlyText }
func CloseUsageMessage() string { return "Usage: /close" }
func CloseFailedMessage() string { return "Could not close this session." }
func CloseResetMessage() string { return "Session history reset." }

func GoalUsageMessage() string { return "Usage:\n/goalkeeper <objective>\n/goalkeeper clear" }
func GoalUnavailableMessage() string { return "Goal control is unavailable right now. Please try again." }
func GoalClearFailedMessage() string { return "Could not clear goal run." }
func GoalStartFailedMessage() string { return "Could not start goal run." }
func GoalAlreadyActiveMessage() string { return "A goal run is already active for this session." }

func TopicDirectMessageOnly() string { return "This command is only available in stream messages." }
func TopicUsageMessage() string { return "Usage: /topic <name>" }
func TopicNotReadyMessage() string { return zulipNotReadyText }
func TopicStreamContextMissingMessage() string { return "Could not determine stream ID from current context." }
func TopicCreateFailedMessage() string { return "Could not create topic session." }
func TopicCreatedFallbackMessage(topicName string) string {
	return fmt.Sprintf("Session created for topic '%s'.", topicName)
}
func TopicCreatedMessage(topicName string) string {
	return fmt.Sprintf("Session created. Post in topic '%s' to continue.", topicName)
}

func ResetUsageMessage(cmd string) string { return fmt.Sprintf("Usage: /%s", cmd) }
func ResetNotReadyMessage() string { return zulipResetNotReadyText }
func ResetFailedMessage() string { return "Could not reset this session." }
func RestartFailedMessage() string { return "Could not restart this session." }
