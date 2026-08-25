package deliveryfmt

const (
	// Transport identifiers intentionally mirror the public locator channel type.
	TransportTelegram   = "telegram"
	TransportSlack      = "slack"
	TransportSlackAgent = "slack_agent"
	TransportZulip      = "zulip"

	DeliveryFormatRichMarkdown DeliveryFormat = "rich_markdown"
	DeliveryFormatRichHTML     DeliveryFormat = "rich_html"
	DeliveryFormatNone         DeliveryFormat = "none"
	DeliveryFormatMrkdwn       DeliveryFormat = "mrkdwn"
	DeliveryFormatMarkdown     DeliveryFormat = "markdown"

	NameTelegramRichMarkdown Name = "telegram_rich_markdown"
	NameTelegramRichHTML     Name = "telegram_rich_html"
	NameSlackMrkdwn          Name = "slack_mrkdwn"
	NameZulipMarkdown        Name = "zulip_markdown"
	NamePlainText            Name = "plain_text"
)

// BuiltinRoutes returns a fresh copy of the current transport route declarations.
// Formatter and prompt registrations are supplied separately at composition time.
func BuiltinRoutes() []Route {
	return []Route{
		{Transport: TransportTelegram, DeliveryFormat: DeliveryFormatNone, RegisteredName: NamePlainText},
		{Transport: TransportSlack, DeliveryFormat: DeliveryFormatMrkdwn, RegisteredName: NameSlackMrkdwn},
		{Transport: TransportSlack, DeliveryFormat: DeliveryFormatNone, RegisteredName: NamePlainText},
		{Transport: TransportSlackAgent, DeliveryFormat: DeliveryFormatMrkdwn, RegisteredName: NameSlackMrkdwn},
		{Transport: TransportSlackAgent, DeliveryFormat: DeliveryFormatNone, RegisteredName: NamePlainText},
		{Transport: TransportZulip, DeliveryFormat: DeliveryFormatMarkdown, RegisteredName: NameZulipMarkdown},
		{Transport: TransportZulip, DeliveryFormat: DeliveryFormatNone, RegisteredName: NamePlainText},
	}
}
