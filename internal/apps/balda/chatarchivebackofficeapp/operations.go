// Package chatarchivebackofficeapp binds passive archive reads to Backoffice.
package chatarchivebackofficeapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
)

// Operations adapts the host archive reader to Backoffice's consuming port.
type Operations struct{ reader *chatarchiveapp.Reader }

// New binds the shared SQL-only reader.
func New(reader *chatarchiveapp.Reader) *Operations { return &Operations{reader: reader} }

// Inventory reads authorized bounded session metadata.
func (o *Operations) Inventory(ctx context.Context, q chatarchivecmd.BrowseQuery, a chatarchivecmd.Authority) (chatarchivecmd.Inventory, error) {
	return o.reader.Inventory(ctx, q, a)
}

// Transcript reads an authorized bounded chat window.
func (o *Operations) Transcript(ctx context.Context, id, cursor string, a chatarchivecmd.Authority) (chatarchivecmd.Transcript, error) {
	return o.reader.Transcript(ctx, id, cursor, a)
}
