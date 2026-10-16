package chatarchiveapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/google/uuid"
)

// ReadStore supplies SQL-only archive queries and current browser authority checks.
type ReadStore interface {
	CheckAuthority(ctx context.Context, authority chatarchivecmd.Authority) error
	List(ctx context.Context, filter chatarchivecmd.Filter) ([]chatarchivecmd.Session, error)
	Summary(ctx context.Context, id string) (chatarchivecmd.Summary, error)
	Messages(ctx context.Context, id string, before int64, limit int) ([]chatarchivecmd.Message, error)
	Successor(ctx context.Context, id string) (string, error)
}

// Reader owns bounded, passive archive browsing policy.
type Reader struct{ store ReadStore }

// NewReader binds the selected database without a runtime or transport accessor.
func NewReader(store ReadStore) *Reader { return &Reader{store: store} }

type browseCursor struct {
	Scope                  string
	Query, Channel, Status string
	At                     time.Time
	ID                     string
	Before                 int64
}

func encodeCursor(c browseCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeCursor(raw string) (browseCursor, error) {
	var c browseCursor
	if len(raw) > 2048 {
		return c, chatarchivecmd.ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, chatarchivecmd.ErrInvalid
	}
	if json.Unmarshal(data, &c) != nil {
		return c, chatarchivecmd.ErrInvalid
	}
	return c, nil
}
func validArchiveID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}
func (r *Reader) check(ctx context.Context, a chatarchivecmd.Authority) error {
	if r == nil || r.store == nil {
		return chatarchivecmd.ErrUnavailable
	}
	return r.store.CheckAuthority(ctx, a)
}

// Inventory returns up to fifty newest sessions with filter-scoped older navigation.
func (r *Reader) Inventory(ctx context.Context, q chatarchivecmd.BrowseQuery, a chatarchivecmd.Authority) (chatarchivecmd.Inventory, error) {
	var result chatarchivecmd.Inventory
	if err := r.check(ctx, a); err != nil {
		return result, err
	}
	if len(q.Query) > 200 || len(q.Channel) > 40 {
		return result, chatarchivecmd.ErrInvalid
	}
	switch q.Status {
	case "", chatarchivecmd.StatusActive, chatarchivecmd.StatusEnded, chatarchivecmd.StatusPending, chatarchivecmd.StatusUncertain:
	default:
		return result, chatarchivecmd.ErrInvalid
	}
	f := chatarchivecmd.Filter{Query: strings.TrimSpace(q.Query), Channel: q.Channel, Status: q.Status, Limit: 51}
	if q.Cursor != "" {
		c, err := decodeCursor(q.Cursor)
		if err != nil {
			return result, err
		}
		if c.Scope != "inventory" || c.Query != f.Query || c.Channel != q.Channel || c.Status != q.Status || c.At.IsZero() || !validArchiveID(c.ID) || c.Before != 0 {
			return result, chatarchivecmd.ErrInvalid
		}
		f.BeforeAt, f.BeforeID = c.At, c.ID
	}
	rows, err := r.store.List(ctx, f)
	if err != nil {
		return result, err
	}
	if len(rows) > 50 {
		rows = rows[:50]
		last := rows[len(rows)-1]
		result.Older = encodeCursor(browseCursor{Scope: "inventory", Query: f.Query, Channel: q.Channel, Status: q.Status, At: last.ObservedAt, ID: last.ID})
	}
	for _, session := range rows {
		summary, err := r.store.Summary(ctx, session.ID)
		if err != nil {
			return chatarchivecmd.Inventory{}, err
		}
		result.Rows = append(result.Rows, summary)
	}
	if err := r.check(ctx, a); err != nil {
		return chatarchivecmd.Inventory{}, err
	}
	return result, nil
}

// Transcript returns the latest hundred messages chronologically within the window.
func (r *Reader) Transcript(ctx context.Context, id, cursor string, a chatarchivecmd.Authority) (chatarchivecmd.Transcript, error) {
	var result chatarchivecmd.Transcript
	if err := r.check(ctx, a); err != nil {
		return result, err
	}
	if !validArchiveID(id) {
		return result, chatarchivecmd.ErrInvalid
	}
	var before int64
	if cursor != "" {
		c, err := decodeCursor(cursor)
		if err != nil {
			return result, err
		}
		if c.Scope != "messages" || c.ID != id || c.Before <= 0 || !c.At.IsZero() || c.Query != "" || c.Channel != "" || c.Status != "" {
			return result, chatarchivecmd.ErrInvalid
		}
		before = c.Before
	}
	summary, err := r.store.Summary(ctx, id)
	if err != nil {
		return result, err
	}
	result.Summary = summary
	result.Messages, err = r.store.Messages(ctx, id, before, 100)
	if err != nil {
		return chatarchivecmd.Transcript{}, err
	}
	if len(result.Messages) > 0 {
		oldest := result.Messages[0].Sequence
		if oldest > 1 {
			older, err := r.store.Messages(ctx, id, oldest, 1)
			if err != nil {
				return chatarchivecmd.Transcript{}, err
			}
			if len(older) > 0 {
				result.Older = encodeCursor(browseCursor{Scope: "messages", ID: id, Before: oldest})
			}
		}
	}
	result.SuccessorID, err = r.store.Successor(ctx, id)
	if err != nil {
		return chatarchivecmd.Transcript{}, err
	}
	// Private execution replay data never crosses this read-policy boundary.
	for i := range result.Messages {
		result.Messages[i].Replay = nil
		result.Messages[i].FinalMembers = nil
		result.Messages[i].Source = ""
	}
	if err := r.check(ctx, a); err != nil {
		return chatarchivecmd.Transcript{}, err
	}
	return result, nil
}
