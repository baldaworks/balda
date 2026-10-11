package webui

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"testing"
)

func TestRendererFactMemoryUsesSharedShell(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer("/balda/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/balda/backoffice/memory", nil), http.StatusOK, "fact-memory", Page{Title: "Fact memory"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body.String(), "Fact memory") || strings.Count(response.Body.String(), `id="main-content"`) != 1 {
		t.Fatal("fact memory did not render in the shared shell")
	}
}

func TestFactMemoryProjectionPreservesTextAndIdentity(t *testing.T) {
	t.Parallel()
	const detailPath = "/memory/3"
	inv := memorycmd.Inventory{Enabled: true, Version: 9, TotalFacts: 2, Facts: []memorycmd.Fact{{ID: 3, Fact: "first\n  multiline fact"}, {ID: 7, Fact: "second fact"}}}
	view := ProjectFactInventory(inv, "FIRST")
	if !view.Enabled || view.TotalFacts != 2 || view.Query != "FIRST" || len(view.Rows) != 2 || view.Rows[0].Fact != inv.Facts[0].Fact || view.Rows[0].DetailPath != detailPath || view.Rows[1].ID != 7 {
		t.Fatalf("inventory projection=%+v", view)
	}
	editor := ProjectFactEditor(memorycmd.Editor{Fact: inv.Facts[0], ExpectedVersion: 9}, false)
	if editor.ID != 3 || editor.Fact != inv.Facts[0].Fact || editor.ExpectedVersion != 9 || editor.New || editor.Action != detailPath || editor.ReopenPath != detailPath {
		t.Fatalf("editor projection=%+v", editor)
	}
	create := ProjectFactEditor(memorycmd.Editor{}, true)
	if !create.New || create.Action != "/memory" || create.ReopenPath != "/memory?new=1" {
		t.Fatalf("create projection=%+v", create)
	}
}

func TestFactMemoryRendererFullFragmentAndSearch(t *testing.T) {
	t.Parallel()
	const fragmentMode = "fragment"
	renderer, err := NewRenderer("/balda/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	view := ProjectFactInventory(memorycmd.Inventory{Enabled: true, TotalFacts: 1, Facts: []memorycmd.Fact{{ID: 2, Fact: "<script>unsafe()</script>\n  multiline fact"}}}, "<query>")
	page := Page{Title: "Fact memory", Current: LocationFactMemory, Navigation: Navigation(usercmd.BackofficeCapabilities{ManageFactMemory: true}, LocationFactMemory), FactMemory: view}
	for _, mode := range []string{"full", fragmentMode, "history"} {
		t.Run(mode, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/balda/backoffice/memory?q=%3Cquery%3E", nil)
			if mode != "full" {
				request.Header.Set("HX-Request", "true")
				request.Header.Set("HX-Target", "main-content")
			}
			if mode == "history" {
				request.Header.Set("HX-History-Restore-Request", "true")
			}
			response := httptest.NewRecorder()
			if err := renderer.Render(response, request, http.StatusOK, TemplateFactMemory, page); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			if strings.Contains(body, "<!doctype html>") != (mode != fragmentMode) || strings.Count(body, `id="main-content"`) != 1 {
				t.Fatalf("wrong %s render shape", mode)
			}
			for _, want := range []string{`<th scope="col">Fact</th>`, `<th scope="col">Actions</th>`, `href="/balda/backoffice/memory/2"`, `href="/balda/backoffice/memory?new=1"`, `action="/balda/backoffice/memory"`, `name="q" value="&lt;query&gt;"`, `&lt;script&gt;unsafe()&lt;/script&gt;` + "\n  multiline fact", `hx-history="false"`, `aria-label="Fact memory table, horizontally scrollable"`} {
				if !strings.Contains(body, want) {
					t.Errorf("%s missing %q", mode, want)
				}
			}
			if strings.Contains(body, "<script>unsafe()") {
				t.Fatal("fact rendered as executable markup")
			}
			if mode != fragmentMode && !strings.Contains(body, `aria-current="page"`) {
				t.Fatal("current Fact memory navigation not marked")
			}
		})
	}
}

func TestFactMemoryRendererEmptyDisabledAndNoResults(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		view    *FactMemoryView
		message string
	}{
		{"empty", &FactMemoryView{Enabled: true}, "No facts yet."},
		{"no results", &FactMemoryView{Enabled: true, TotalFacts: 2, Query: "missing"}, "No facts match your search."},
		{"disabled", &FactMemoryView{}, "Fact memory is disabled in configuration."},
		{"disabled add editor", &FactMemoryView{Editor: ProjectFactEditor(memorycmd.Editor{}, true)}, "Fact memory is disabled in configuration."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/memory", nil), http.StatusOK, TemplateFactMemory, Page{Title: "Fact memory", FactMemory: tc.view}); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			if !strings.Contains(body, tc.message) {
				t.Fatalf("missing state %q", tc.message)
			}
			if tc.view.Enabled && !strings.Contains(body, ">Add fact</a>") {
				t.Fatal("enabled memory offers no create action")
			}
			if !tc.view.Enabled && (strings.Contains(body, `name="q"`) || strings.Contains(body, `name="fact"`) || strings.Contains(body, ">Add fact</a>")) {
				t.Fatal("disabled memory exposes operative controls")
			}
		})
	}
}

func TestFactMemoryEditorPreservesConflictAndFailureStatus(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer("/balda/backoffice")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable} {
		for _, fragment := range []bool{false, true} {
			t.Run(strconv.Itoa(status)+"/"+strconv.FormatBool(fragment), func(t *testing.T) {
				view := &FactMemoryView{Enabled: true, Editor: ProjectFactEditor(memorycmd.Editor{Fact: memorycmd.Fact{ID: 4, Fact: "submitted <fact>\nunchanged text"}, ExpectedVersion: 8}, false)}
				page := Page{Title: "Edit fact", FactMemory: view, CSRFToken: "safe-csrf", Error: &ErrorView{Heading: "Change not saved", Message: "Reopen this fact before retrying."}}
				request := httptest.NewRequest(http.MethodPost, "/balda/backoffice/memory/4", nil)
				if fragment {
					request.Header.Set("HX-Request", "true")
					request.Header.Set("HX-Target", "main-content")
				}
				response := httptest.NewRecorder()
				if err := renderer.Render(response, request, status, TemplateFactMemory, page); err != nil {
					t.Fatal(err)
				}
				body := response.Body.String()
				if response.Code != status || strings.Count(body, `id="main-content"`) != 1 {
					t.Fatalf("status/shape=%d", response.Code)
				}
				for _, want := range []string{`<label class="form-label" for="fact-text">Fact</label>`, `name="fact"`, `submitted &lt;fact&gt;` + "\nunchanged text", `name="expected_version" value="8"`, `name="csrf_token" value="safe-csrf"`, `action="/balda/backoffice/memory/4"`, `action="/balda/backoffice/memory/4/delete"`, `name="confirm" value="yes"`, `Confirm deleting this fact.`, `href="/balda/backoffice/memory/4"`, `Reopen this fact`, `Save fact`, `Cancel`, `Conversation history remains.`} {
					if !strings.Contains(body, want) {
						t.Errorf("editor missing %q", want)
					}
				}
			})
		}
	}
	create := &FactMemoryView{Enabled: true, Editor: ProjectFactEditor(memorycmd.Editor{}, true)}
	response := httptest.NewRecorder()
	if err := renderer.Render(response, httptest.NewRequest(http.MethodGet, "/memory?new=1", nil), http.StatusOK, TemplateFactMemory, Page{Title: "Add fact", FactMemory: create}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body.String(), ">Create fact</button>") || strings.Contains(response.Body.String(), `name="expected_version"`) || strings.Contains(response.Body.String(), ">Delete fact</button>") {
		t.Fatal("create form does not match creation operation")
	}
}
