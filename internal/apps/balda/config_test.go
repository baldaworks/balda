package balda

import (
	"math"
	"testing"
)

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
