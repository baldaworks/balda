---
apiVersion: callee.metalagman.dev/v1alpha1
kind: Loop
spec:
  description: >
    Maintains Balda documentation through iterative technical writing and
    independent evidence-based editing.
  children:
    - ref: doc-maintenance/roles/technical-writer
      alias: writer
      input: |
        Documentation maintenance request:
        {{ .Input }}

        {{ with index .State.outputs "editor" }}
        Previous independent editorial review:
        {{ . }}

        Address every material finding before returning the updated result.
        {{ end }}
    - ref: doc-maintenance/roles/editor
      alias: editor
      canEscalate: true
      input: |
        Documentation maintenance request:
        {{ .Input }}

        Writer report:
        {{ index .State.outputs "writer" }}

        Independently inspect the repository result. If it fully satisfies the
        request and the documentation maintenance contract, return approval and
        escalate to finish the loop. Otherwise return actionable findings for
        the next writing iteration.
  maxIterations: 5
  onExhausted: fail
  output: |
    Documentation maintenance completed:
    {{ index .State.outputs "editor" }}
---
{{ .Input }}
