package balda

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/schedulebackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/scheduledjobs"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
)

// This opt-in browser gate exercises real schedule management and SQLite through Backoffice.
func TestBackofficeSchedulesBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, basePath := range []string{"", "/balda"} {
		t.Run(basePath, func(t *testing.T) {
			provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = provider.Close() })
			hash, err := userpassword.Hash([]byte("correct horse battery staple"))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			admin := usercmd.User{ID: "administrator", Username: "administrator", NormalizedUsername: "administrator",
				DisplayName: "administrator", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive,
				Primary: true, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
				Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := provider.Users().CreateUser(t.Context(), admin,
				usercmd.CredentialSecret{UserID: admin.ID, PasswordHash: hash},
				usercmd.AuditEvent{ID: "schedule-browser-admin", Action: usercmd.AuditActionUserCreated,
					Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser,
					TargetID: admin.ID, Source: "schedule-browser-fixture", OccurredAt: now}); err != nil {
				t.Fatal(err)
			}
			for _, job := range []state.ScheduledJobRecord{
				{JobID: "daily-summary", Source: state.ScheduledJobSourceManaged, Enabled: true, DefinitionVersion: 2,
					TargetKind: "locator", TargetKey: "telegram:9001:0", SessionID: "tg-9001-0",
					ChannelType: state.ChannelTypeTelegram, AddressKey: "9001:0", AddressJSON: `{}`,
					Content: "Summarize yesterday's work", ScheduleSpec: "0 9 * * *", Timezone: "UTC",
					Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(12 * time.Hour), CreatedAt: now, UpdatedAt: now},
				{JobID: "config:morning", Source: state.ScheduledJobSourceConfig, Enabled: true, DefinitionVersion: 1,
					TargetKind: "locator", TargetKey: "telegram:9001:0", SessionID: "tg-9001-0",
					ChannelType: state.ChannelTypeTelegram, AddressKey: "9001:0", AddressJSON: `{}`,
					Content: "Review the morning queue", ScheduleSpec: "0 8 * * *", Timezone: "UTC",
					Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(11 * time.Hour), CreatedAt: now, UpdatedAt: now},
			} {
				if err := provider.ScheduledJobs().Upsert(t.Context(), job); err != nil {
					t.Fatal(err)
				}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			runtime, err := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{
				ListenAddr: address, PublicURL: "http://" + address, BasePath: basePath,
				AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour}}, provider)
			if err != nil {
				t.Fatal(err)
			}
			management := scheduledjobs.NewManagement(provider.ScheduledJobs(), provider.ScheduleManagement(),
				provider.ScheduleRuns(), provider.Jobs(), provider.Jobs())
			if err := runtime.ConfigureSchedulesOperations(schedulebackofficeapp.New(management)); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runtime.Stop(context.Background()) })
			root, err := filepath.Abs("../../..")
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/schedules.cjs", "http://"+address+basePath)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("Schedules browser workflow: %v\n%s", err, output)
			}
			for _, id := range []string{"weekly-review-desktop", "weekly-review-mobile"} {
				created, found, err := provider.ScheduledJobs().GetByID(t.Context(), id)
				if err != nil || !found || created.Source != state.ScheduledJobSourceManaged {
					t.Fatalf("managed schedule %s was not saved: %+v, found=%t, err=%v", id, created, found, err)
				}
				if created.ReportToEnabled != (id == "weekly-review-desktop") {
					t.Fatalf("managed schedule %s report selection = %t", id, created.ReportToEnabled)
				}
			}
			for _, id := range []string{"daily-summary", "config:morning"} {
				runs, err := provider.ScheduleRuns().ListBySchedule(t.Context(), id, time.Time{}, "", 10)
				if err != nil || len(runs) != 2 || runs[0].Trigger != state.ScheduleRunTriggerManual || runs[1].Trigger != state.ScheduleRunTriggerManual {
					t.Fatalf("manual run for %s was not saved: %+v, err=%v", id, runs, err)
				}
			}
			t.Log(string(output))
		})
	}
}
