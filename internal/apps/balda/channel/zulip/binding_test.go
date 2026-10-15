package zulip

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestZulipBindingIdentityRejectsUnverifiedBot(t *testing.T) {
	for _, body := range []string{
		`{"result":"success","user_id":1,"email":"bot@fixture.example","is_active":true,"is_bot":false}`,
		`{"result":"success","user_id":1,"email":"bot@fixture.example","is_active":false,"is_bot":true}`,
		`{"result":"success","user_id":1,"email":"other@fixture.example","is_active":true,"is_bot":true}`,
		`{"result":"error","msg":"Invalid API key"}`,
	} {
		t.Run(body, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
			defer api.Close()
			if _, err := NewClient(api.URL, "bot@fixture.example", "synthetic-key").BindingIdentity(t.Context()); err == nil {
				t.Fatal("unverified bot identity accepted")
			}
		})
	}
}
