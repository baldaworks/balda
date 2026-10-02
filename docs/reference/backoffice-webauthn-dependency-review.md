# Backoffice WebAuthn dependency review

Balda adapts the AlaTooGuide Backoffice adapter and standard registration
vector from source revision `2ffe95afe4fabad3ab8a4733d0559deb2db26262`.
The pinned verifier is `github.com/go-webauthn/webauthn v0.18.1`.
Balda derives the RP ID from the exact configured public origin; it does not
reuse AlaTooGuide's mandatory enrollment policy or parent-domain RP aliases.

The library owns challenge, origin, RP hash, signature and authenticator
verification. Balda requires user verification on registration and assertion,
rejects clone warnings and changes to backup eligibility, and persists counter
and backup state only with a successful transactional authority transition.
Zero-to-zero counters remain valid for synchronized authenticators. A previously
nonzero counter cannot regress to zero.

Balda's ceremony state is bounded, hashed-handle addressed, browser/CSRF/user/
purpose/session/authority bound, expiring, and atomically consumed before
cryptographic verification. No authenticator private key is stored.

On 2026-10-02, `go tool govulncheck -show verbose
./internal/apps/backoffice/security` reported zero reachable or imported-package
vulnerabilities. The module-only advisory `GO-2026-5932` applies to the
unmaintained `golang.org/x/crypto/openpgp` package, which this security path
does not import. Its module remains required for the existing password hash
implementation and verifier dependencies; the advisory has no fixed version.
This is the result for that package scope and date, not a permanent guarantee
about every application dependency.

Verification includes the W3C none/ES256 registration vector, real generated
ES256 assertion signatures, and rejection of wrong origin/RP/challenge/signature,
missing user verification, clone counters, changed backup eligibility, browser
mismatch and consumed ceremony reuse. Rerun the vulnerability scan and these
tests whenever updating the verifier or its dependencies.
