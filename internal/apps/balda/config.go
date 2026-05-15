package balda

// Config holds the configuration for the Balda bot.
type Config struct {
	Balda BaldaConfig `mapstructure:"balda"`
}

// BaldaConfig holds the balda-specific configuration.
type BaldaConfig struct {
	Provider          string                   `mapstructure:"provider"`
	Telegram          TelegramConfig           `mapstructure:"telegram"`
	InboundWebhooks   InboundWebhooksConfig    `mapstructure:"inbound_webhooks"`
	Logger            LoggerConfig             `mapstructure:"logger"`
	WorkingDir        string                   `mapstructure:"working_dir"`
	StateDir          string                   `mapstructure:"state_dir"`
	Sessions          SessionsConfig           `mapstructure:"sessions"`
	Memory            MemoryConfig             `mapstructure:"memory"`
	Goal              GoalConfig               `mapstructure:"goal"`
	Locators          map[string]LocatorConfig `mapstructure:"locators"`
	Scheduler         SchedulerConfig          `mapstructure:"scheduler"`
	Workspace         WorkspaceConfig          `mapstructure:"workspace"`
	MCPServers        []string                 `mapstructure:"mcp_servers"`
	GlobalInstruction string                   `mapstructure:"global_instruction"`
}

// TelegramConfig holds the Telegram bot configuration.
type TelegramConfig struct {
	Token          string        `mapstructure:"token"`
	FormattingMode string        `mapstructure:"formatting_mode"`
	PlanUpdates    bool          `mapstructure:"plan_updates"`
	Webhook        WebhookConfig `mapstructure:"webhook"`
}

// WebhookConfig holds Telegram webhook receiver settings.
type WebhookConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	ListenAddr string `mapstructure:"listen_addr"`
	Path       string `mapstructure:"path"`
	URL        string `mapstructure:"url"`
	AuthToken  string `mapstructure:"auth_token"`
}

// InboundWebhooksConfig controls generic inbound webhook ingestion.
type InboundWebhooksConfig struct {
	Enabled    bool                                 `mapstructure:"enabled"`
	ListenAddr string                               `mapstructure:"listen_addr"`
	Routes     map[string]InboundWebhookRouteConfig `mapstructure:"routes"`
}

// InboundWebhookRouteConfig binds a webhook path to a target session alias.
type InboundWebhookRouteConfig struct {
	Path           string `mapstructure:"path"`
	ReportTo       string `mapstructure:"report_to"`
	PromptTemplate string `mapstructure:"prompt_template"`
}

// LoggerConfig holds the logger configuration.
type LoggerConfig struct {
	Level  string `mapstructure:"level"`
	Pretty bool   `mapstructure:"pretty"`
}

// WorkspaceConfig controls balda Git workspace behavior.
type WorkspaceConfig struct {
	Mode       string `mapstructure:"mode"`
	BaseBranch string `mapstructure:"base_branch"`
}

type SessionsConfig struct {
	Persistence string `mapstructure:"persistence"`
}

type MemoryConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// GoalConfig controls /goal command execution behavior.
type GoalConfig struct {
	MaxIterations int `mapstructure:"max_iterations"`
}

// LocatorConfig defines a canonical session locator alias.
type LocatorConfig struct {
	ChannelType string `mapstructure:"channel_type"`
	AddressKey  string `mapstructure:"address_key"`
	AddressJSON string `mapstructure:"address_json"`
	SessionID   string `mapstructure:"session_id"`
}

// SchedulerConfig controls startup-managed recurring jobs.
type SchedulerConfig struct {
	Jobs []ScheduledJobConfig `mapstructure:"jobs"`
}

// ScheduledJobConfig defines a config-managed recurring job.
type ScheduledJobConfig struct {
	ID     string `mapstructure:"id"`
	Alias  string `mapstructure:"alias"`
	Cron   string `mapstructure:"cron"`
	Prompt string `mapstructure:"prompt"`
}
