package webui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const (
	TemplateLogin    = "login"
	TemplateOverview = "overview"
	TemplateAccess   = "access"
	TemplateAccount  = "account"
	TemplateAudit    = "audit"
	TemplateError    = "error"
	TemplateRefresh  = "refresh"
	TemplatePassword = "password"
	TemplateGallery  = "gallery"
)

var templateFiles = map[string]string{
	TemplateLogin: "templates/login.tmpl", TemplateOverview: "templates/overview.tmpl",
	TemplateAccess: "templates/access.tmpl", TemplateAccount: "templates/account.tmpl",
	TemplateAudit: "templates/audit.tmpl", TemplateError: "templates/error.tmpl",
	TemplateRefresh: "templates/refresh.tmpl", TemplatePassword: "templates/password.tmpl",
	TemplateGallery: "templates/gallery.tmpl",
}

//go:embed templates static
var embedded embed.FS

// Renderer buffers allowlisted templates before committing an HTTP response.
type Renderer struct {
	templates map[string]*template.Template
}

// NewRenderer parses each allowlisted page independently with the shared shell.
func NewRenderer(basePath string) (*Renderer, error) {
	return newRenderer(basePath, basePath)
}

// NewQARenderer keeps preview navigation inside QA while sharing production templates.
func NewQARenderer(basePath string) (*Renderer, error) {
	return newRenderer(basePath+"/qa/ui", basePath)
}

func newRenderer(pagePath, assetPath string) (*Renderer, error) {
	templates := make(map[string]*template.Template, len(templateFiles))
	for name, pageFile := range templateFiles {
		parsed, err := template.New(name).Funcs(template.FuncMap{
			"path":      func(route any) string { return pagePath + fmt.Sprint(route) },
			"assetPath": func(route any) string { return assetPath + fmt.Sprint(route) },
		}).ParseFS(embedded, "templates/document.tmpl", "templates/fragment.tmpl", pageFile)
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", name, err)
		}
		templates[name] = parsed
	}
	return &Renderer{templates: templates}, nil
}

// Render writes a full document or exact main-content fragment after successful buffering.
func (r *Renderer) Render(w http.ResponseWriter, request *http.Request, status int, name string, page Page) error {
	if status < 100 || status > 599 {
		return fmt.Errorf("render status is invalid")
	}
	if err := page.validate(); err != nil {
		return err
	}
	tmpl, ok := r.templates[name]
	if !ok {
		return fmt.Errorf("template %q is not allowlisted", name)
	}
	root := "document"
	if EligibleFragment(request) {
		root = "fragment"
	}
	var buffer bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buffer, root, page); err != nil {
		return fmt.Errorf("render %s template: %w", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := w.Write(buffer.Bytes())
	return err
}

// EligibleFragment implements the exact HTMX fragment negotiation contract.
func EligibleFragment(request *http.Request) bool {
	return request.Header.Get("HX-Request") == "true" &&
		request.Header.Get("HX-Target") == "main-content" &&
		request.Header.Get("HX-History-Restore-Request") != "true"
}

// RespondMutation emits 204/HX-Location for eligible HTMX requests and a native 303 otherwise.
func RespondMutation(w http.ResponseWriter, request *http.Request, location Location) error {
	return RespondMutationAt(w, request, location, "")
}

// RespondMutationAt emits a mutation response at a configured public base path.
func RespondMutationAt(w http.ResponseWriter, request *http.Request, location Location, basePath string) error {
	if !location.Valid() {
		return fmt.Errorf("mutation location is invalid")
	}
	if EligibleFragment(request) {
		w.Header().Set("HX-Location", basePath+string(location))
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	w.Header().Set("Location", basePath+string(location))
	w.WriteHeader(http.StatusSeeOther)
	return nil
}

// Assets returns the embedded offline static asset handler.
func Assets() (http.Handler, error) {
	root, err := fs.Sub(embedded, "static")
	if err != nil {
		return nil, fmt.Errorf("open embedded static assets: %w", err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasPrefix(request.URL.Path, "/assets/") {
			http.NotFound(w, request)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(request.URL.Path, "/assets/")), "/")
		info, err := fs.Stat(root, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, request)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		servedRequest := request.Clone(request.Context())
		servedRequest.URL.Path = "/" + name
		files.ServeHTTP(w, servedRequest)
	}), nil
}
