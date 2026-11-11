package pluginbackofficeapp

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

type projectionOwner struct {
	Owner
	items     []plugincmd.InstalledView
	err       error
	authority plugincmd.Authority
}

func (o *projectionOwner) ManagementInventory(_ context.Context, a plugincmd.Authority) ([]plugincmd.InstalledView, error) {
	o.authority = a
	return o.items, o.err
}
func (o *projectionOwner) ManagementDetail(_ context.Context, _ string, a plugincmd.Authority) (plugincmd.PluginDetail, error) {
	o.authority = a
	return plugincmd.PluginDetail{InstalledView: o.items[0], Commands: []plugincmd.Contribution{{Name: "ship", Description: "Ship safely"}}}, o.err
}

type projectionHealth struct{}

func (projectionHealth) Status(name, revision string) plugincmd.RuntimeStatus {
	return plugincmd.RuntimeStatus{SnapshotID: name + ":" + revision, ReadyMCPServers: 1}
}
func TestReadProjectionKeepsDisabledContributionsWithoutHealth(t *testing.T) {
	owner := &projectionOwner{items: []plugincmd.InstalledView{{PluginStatus: plugincmd.PluginStatus{Name: "demo", Revision: "r1"}}}}
	authority := plugincmd.Authority{SessionID: "browser"}
	detail, err := New(owner, nil).Detail(t.Context(), "demo", authority)
	if err != nil || detail.Enabled || len(detail.Commands) != 1 || owner.authority != authority {
		t.Fatalf("detail %+v %v", detail, err)
	}
	if len(detail.Runtime.DiagnosticCodes) != 1 || detail.Runtime.DiagnosticCodes[0] != "catalog_unavailable" {
		t.Fatal("missing health claimed ready")
	}
	inventory, err := New(owner, projectionHealth{}).Inventory(t.Context(), authority)
	if err != nil || inventory[0].Runtime.SnapshotID != "demo:r1" {
		t.Fatalf("inventory %+v %v", inventory, err)
	}
	owner.err = plugincmd.ErrUnavailable
	if _, err := New(owner, projectionHealth{}).Inventory(t.Context(), authority); !errors.Is(err, plugincmd.ErrUnavailable) {
		t.Fatal("inventory error hidden as empty")
	}
}
