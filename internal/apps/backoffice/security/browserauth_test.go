package security

import "github.com/baldaworks/balda/internal/apps/balda/browserauth"

const validPassword = "correct horse battery staple"

type Credentials = browserauth.Credentials
type Principal = browserauth.Principal
type CeremonyStart = browserauth.CeremonyStart

var (
	ErrForbidden         = browserauth.ErrForbidden
	ErrRefreshConcurrent = browserauth.ErrRefreshConcurrent
	ErrUnauthenticated   = browserauth.ErrUnauthenticated
)
