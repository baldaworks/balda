.PHONY: dev scenarios runtime-state projection-replay

dev:
	@./scripts/dev/run-balda-embedded-runtime.sh

scenarios:
	@./scripts/dev/run-fake-ingress-scenarios.sh

runtime-state:
	@./scripts/dev/dump-runtime-state.sh

projection-replay:
	@./scripts/dev/replay-events-into-projections.sh
