---
apiVersion: callee.metalagman.dev/v1alpha1
kind: Loop
spec:
  description: >
    Creates or updates a Balda ADR through iterative technical writing and
    independent evidence-based review.
  children:
    - ref: adr-maintenance/roles/technical-writer
      alias: writer
      input: |
        ADR maintenance request:
        {{ .Input }}

        Create or update the appropriate Balda ADR and architecture index.

        {{ with index .State.outputs "reviewer" }}
        Previous independent review:
        {{ . }}

        Address every material finding before returning the updated result.
        {{ end }}
    - ref: adr-maintenance/roles/reviewer
      alias: reviewer
      canEscalate: true
      input: |
        ADR maintenance request:
        {{ .Input }}

        Writer report:
        {{ index .State.outputs "writer" }}

        Independently inspect the repository result. If it fully satisfies the
        request and the ADR maintenance contract, return approval and escalate
        to finish the loop. Otherwise return actionable findings normally for
        the next writing iteration.
  maxIterations: 5
  onExhausted: fail
  output: |
    ADR maintenance completed:
    {{ index .State.outputs "reviewer" }}
---
{{ .Input }}
