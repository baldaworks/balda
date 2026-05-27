.PHONY: dev scenarios jetstream-state

dev:
	@./scripts/dev/run-balda-embedded-jetstream.sh

scenarios:
	@./scripts/dev/run-fake-ingress-scenarios.sh

jetstream-state:
	@./scripts/dev/dump-jetstream-state.sh
