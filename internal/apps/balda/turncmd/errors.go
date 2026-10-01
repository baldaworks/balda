package turncmd

// PreparationError identifies a failure before the provider executor was called.
// Its cause is for operator diagnostics, never for transport delivery.
type PreparationError struct {
	Cause error
}

func (e *PreparationError) Error() string { return e.Cause.Error() }
func (e *PreparationError) Unwrap() error { return e.Cause }
