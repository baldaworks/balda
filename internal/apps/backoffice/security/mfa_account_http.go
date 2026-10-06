package security

import (
	"context"
	"net/http"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type accountMFAService interface {
	BeginEnable(ctx context.Context, token string, password []byte, browser, csrf string) (CeremonyStart, error)
	FinishEnable(ctx context.Context, token, transaction, browser, csrf string, response []byte) (Credentials, error)
	BeginReplace(ctx context.Context, token string, password []byte, confirmed bool, browser, csrf string) (CeremonyStart, error)
	FinishReplace(ctx context.Context, token, transaction, browser, csrf string, response []byte) (Credentials, error)
	Disable(ctx context.Context, token string, password []byte, confirmed bool) (Credentials, error)
}

// BeginEnable starts password-confirmed opt-in registration.
func (b *Browser) BeginEnable(w http.ResponseWriter, r *http.Request) {
	b.accountMFAStart(w, r, usercmd.MFAEnable)
}

// FinishEnable commits the verified optional factor.
func (b *Browser) FinishEnable(w http.ResponseWriter, r *http.Request) {
	b.accountMFAFinish(w, r, usercmd.MFAEnable)
}

// BeginReplace starts password-confirmed replacement registration.
func (b *Browser) BeginReplace(w http.ResponseWriter, r *http.Request) {
	b.accountMFAStart(w, r, usercmd.MFAReplace)
}

// FinishReplace commits a verified replacement registration.
func (b *Browser) FinishReplace(w http.ResponseWriter, r *http.Request) {
	b.accountMFAFinish(w, r, usercmd.MFAReplace)
}

// Disable removes the factor and installs a new password-authenticated family.
func (b *Browser) Disable(w http.ResponseWriter, r *http.Request) {
	form, ok := b.mutationForm(w, r)
	if !ok {
		return
	}
	access, err := r.Cookie(AccessCookieName)
	if err != nil {
		b.writeServiceError(w, r, ErrUnauthenticated)
		return
	}
	if err := b.service.ValidateCSRF(r.Context(), access.Value, form.Get("csrf_token")); err != nil {
		b.writeServiceError(w, r, err)
		return
	}
	s, ok := b.service.(accountMFAService)
	if !ok {
		b.writeServiceError(w, r, ErrMFAUnavailable)
		return
	}
	password := []byte(form.Get("password"))
	defer zero(password)
	credentials, err := s.Disable(r.Context(), access.Value, password, form.Get("confirm") == "on")
	if err != nil {
		b.writeServiceError(w, r, err)
		return
	}
	b.setCredentials(w, credentials)
	b.redirect(w, r, "", b.path("/account"))
}

func (b *Browser) accountMFAStart(w http.ResponseWriter, r *http.Request, purpose usercmd.MFAPurpose) {
	form, ok := b.mutationForm(w, r)
	if !ok {
		return
	}
	access, err := r.Cookie(AccessCookieName)
	if err != nil {
		b.writeServiceError(w, r, ErrUnauthenticated)
		return
	}
	if err := b.service.ValidateCSRF(r.Context(), access.Value, form.Get("csrf_token")); err != nil {
		b.writeServiceError(w, r, err)
		return
	}
	s, ok := b.service.(accountMFAService)
	if !ok {
		b.writeServiceError(w, r, ErrMFAUnavailable)
		return
	}
	browser, err := b.newMFABrowser(w)
	if err != nil {
		b.writeServiceError(w, r, err)
		return
	}
	password := []byte(form.Get("password"))
	defer zero(password)
	var start CeremonyStart
	switch purpose {
	case usercmd.MFAEnable:
		start, err = s.BeginEnable(r.Context(), access.Value, password, browser, form.Get("csrf_token"))
	case usercmd.MFAReplace:
		start, err = s.BeginReplace(r.Context(), access.Value, password, form.Get("confirm") == "on", browser, form.Get("csrf_token"))
	}
	if err != nil {
		b.writeServiceError(w, r, err)
		return
	}
	b.respondCeremony(w, r, purpose, start)
}

func (b *Browser) accountMFAFinish(w http.ResponseWriter, r *http.Request, purpose usercmd.MFAPurpose) {
	form, ok := b.mutationFormLimit(w, r, 128<<10)
	if !ok {
		return
	}
	access, err := r.Cookie(AccessCookieName)
	if err != nil {
		b.writeServiceError(w, r, ErrUnauthenticated)
		return
	}
	if err := b.service.ValidateCSRF(r.Context(), access.Value, form.Get("csrf_token")); err != nil {
		b.writeServiceError(w, r, err)
		return
	}
	s, ok := b.service.(accountMFAService)
	if !ok {
		b.writeServiceError(w, r, ErrMFAUnavailable)
		return
	}
	var credentials Credentials
	response := []byte(form.Get("credential"))
	switch purpose {
	case usercmd.MFAEnable:
		credentials, err = s.FinishEnable(r.Context(), access.Value, form.Get("transaction"), mfaCookie(r), form.Get("csrf_token"), response)
	case usercmd.MFAReplace:
		credentials, err = s.FinishReplace(r.Context(), access.Value, form.Get("transaction"), mfaCookie(r), form.Get("csrf_token"), response)
	}
	if err != nil {
		b.writeServiceError(w, r, err)
		return
	}
	b.clearCookie(w, MFACookieName, b.path("/"))
	b.setCredentials(w, credentials)
	b.redirect(w, r, "", b.path("/account"))
}
