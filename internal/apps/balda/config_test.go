package balda

import (
	"math"
	"strings"
	"testing"

	"github.com/normahq/runtime/v2/appconfig"
)

func TestWebhookConfigRejectsUnknownFieldsAfterDecode(t *testing.T) {
	for _, tt := range []struct {
		name     string
		envelope map[string]any
		wantErr  bool
	}{
		{name: "current report reference", envelope: map[string]any{"report_to": map[string]any{"target": "managed_alias", "key": "main_chat"}}},
		{name: "unknown envelope field", envelope: map[string]any{"unexpected": true}, wantErr: true},
		{name: "unknown report field", envelope: map[string]any{"report_to": map[string]any{"target": "locator", "key": "telegram:9001:0", "unexpected": true}}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var decoded struct {
				Balda BaldaConfig `mapstructure:"balda"`
			}
			settings := map[string]any{"balda": map[string]any{"webhooks": map[string]any{"routes": map[string]any{"event": map[string]any{"envelope": tt.envelope}}}}}
			if err := appconfig.DecodeSettings(settings, &decoded); err != nil {
				t.Fatal(err)
			}
			err := validateWebhookRawConfig(decoded.Balda.Webhooks)
			if tt.wantErr != (err != nil) {
				t.Fatalf("validateWebhookRawConfig() = %v", err)
			}
			if err != nil && (!strings.Contains(err.Error(), "unsupported fields") || strings.Contains(err.Error(), "unexpected")) {
				t.Fatalf("unbounded config error = %v", err)
			}
		})
	}
}

func TestAttachmentsConfigLimits(t *testing.T) {
	tests := []struct {
		name    string
		config  AttachmentsConfig
		wantErr bool
	}{
		{
			name: "valid",
			config: AttachmentsConfig{
				MaxFilesPerMessage: defaultAttachmentMaxFilesPerMessage,
				MaxFileBytes:       defaultAttachmentMaxFileBytes,
				MaxTotalBytes:      defaultAttachmentMaxTotalBytes,
			},
		},
		{
			name: "file count must be positive",
			config: AttachmentsConfig{
				MaxFileBytes:  defaultAttachmentMaxFileBytes,
				MaxTotalBytes: defaultAttachmentMaxTotalBytes,
			},
			wantErr: true,
		},
		{
			name: "file bytes must be positive",
			config: AttachmentsConfig{
				MaxFilesPerMessage: defaultAttachmentMaxFilesPerMessage,
				MaxTotalBytes:      defaultAttachmentMaxTotalBytes,
			},
			wantErr: true,
		},
		{
			name: "file bytes must leave room for overflow detection",
			config: AttachmentsConfig{
				MaxFilesPerMessage: defaultAttachmentMaxFilesPerMessage,
				MaxFileBytes:       math.MaxInt64,
				MaxTotalBytes:      math.MaxInt64,
			},
			wantErr: true,
		},
		{
			name: "total bytes must be positive",
			config: AttachmentsConfig{
				MaxFilesPerMessage: defaultAttachmentMaxFilesPerMessage,
				MaxFileBytes:       defaultAttachmentMaxFileBytes,
			},
			wantErr: true,
		},
		{
			name: "total must cover one file",
			config: AttachmentsConfig{
				MaxFilesPerMessage: defaultAttachmentMaxFilesPerMessage,
				MaxFileBytes:       defaultAttachmentMaxFileBytes,
				MaxTotalBytes:      defaultAttachmentMaxFileBytes - 1,
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits, err := test.config.Limits()
			if test.wantErr {
				if err == nil {
					t.Fatalf("Limits() error = nil, want failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("Limits() error = %v", err)
			}
			if limits.MaxFilesPerMessage != defaultAttachmentMaxFilesPerMessage || limits.MaxFileBytes != defaultAttachmentMaxFileBytes || limits.MaxTotalBytes != defaultAttachmentMaxTotalBytes {
				t.Fatalf("Limits() = %+v, want configured defaults", limits)
			}
		})
	}
}

func TestWebhookConfigSlugIdentity(t *testing.T) {
	for _, tt := range []struct {
		name    string
		key     string
		enabled bool
		wantErr string
	}{
		{name: "disabled path-free route", key: "orders"},
		{name: "enabled path-free route", key: "orders", enabled: true},
		{name: "disabled invalid slug", key: "orders/events", wantErr: "orders/events"},
		{name: "enabled invalid slug", key: "orders/events", enabled: true, wantErr: "orders/events"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			route := map[string]any{"prompt_template": "{{.RawBody}}"}
			var decoded struct {
				Balda BaldaConfig `mapstructure:"balda"`
			}
			settings := map[string]any{"balda": map[string]any{"webhooks": map[string]any{"enabled": tt.enabled, "routes": map[string]any{tt.key: route}}}}
			if err := appconfig.DecodeSettings(settings, &decoded); err != nil {
				t.Fatal(err)
			}
			err := validateWebhookRawConfig(decoded.Balda.Webhooks)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validation = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
