# Management API v1 contract

`openapi.yaml` is the authoritative wire contract. The generated Go boundary
is committed at `internal/managementapi/v1/management.gen.go`; do not edit it
by hand.

Regenerate the boundary:

```bash
go generate ./internal/managementapi/v1
```

Validate the contract, deterministic generation, generated code, and boundary
tests:

```bash
./scripts/ci/check-management-openapi.sh
go test -race ./internal/managementapi/v1
go tool go-arch-lint check --project-path .
```

This Story defines contracts only. No management route is reachable until the
loopback server and its authentication middleware are added by later Stories.
