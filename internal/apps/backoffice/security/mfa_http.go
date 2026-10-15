package security

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// MFACookieName is an unauthenticated browser binding, never an access credential.
const MFACookieName = "balda_webauthn"

type mfaBrowserService interface {
	FinishLogin(ctx context.Context, transaction, browser, csrf string, response []byte) (Credentials, error)
}

func (b *Browser) newMFABrowser(w http.ResponseWriter) (string, error) {
	value, err := randomValue(b.random, 32)
	if err != nil {
		return "", err
	}
	b.setCookie(w, MFACookieName, value, b.path("/"), time.Now().UTC().Add(15*time.Minute), true)
	return value, nil
}

func mfaCookie(r *http.Request) string {
	cookie, err := r.Cookie(MFACookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (b *Browser) respondCeremony(w http.ResponseWriter, r *http.Request, purpose usercmd.MFAPurpose, start CeremonyStart) {
	w.Header().Set("Cache-Control", "no-store")
	if b.ceremonyResponder != nil {
		b.ceremonyResponder(w, r, purpose, start)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(start)
}

// FinishLogin completes only the browser-bound pending assertion.
func (b *Browser) FinishLogin(w http.ResponseWriter, r *http.Request) {
	form, ok := b.mutationFormLimit(w, r, 128<<10)
	if !ok {
		return
	}
	s, ok := b.service.(mfaBrowserService)
	if !ok {
		b.writeServiceError(w, r, ErrMFAUnavailable)
		return
	}
	credentials, err := s.FinishLogin(withSessionClient(r), form.Get("transaction"), mfaCookie(r), form.Get("csrf_token"), []byte(form.Get("credential")))
	if err != nil {
		b.writeServiceError(w, r, err)
		return
	}
	b.clearCookie(w, MFACookieName, b.path("/"))
	b.setCredentials(w, credentials)
	if credentials.Assurance == usercmd.SessionAssuranceRestricted {
		b.redirect(w, r, "", b.path("/account/password"))
		return
	}
	b.redirect(w, r, form.Get("return_to"), b.path("/overview"))
}
