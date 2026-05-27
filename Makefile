.PHONY: dev scenarios jetstream-state projection-replay

dev:
	@./scripts/dev/run-balda-embedded-jetstream.sh

scenarios:
	@./scripts/dev/run-fake-ingress-scenarios.sh

jetstream-state:
	@./scripts/dev/dump-jetstream-state.sh

projection-replay:
	@./scripts/dev/replay-events-into-projections.sh
