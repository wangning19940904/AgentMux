package core

// SteerRejectedError means the runtime positively confirmed that no input was
// accepted. All other errors are ambiguous and must never trigger a replay.
type SteerRejectedError struct{ Reason string }

func (e *SteerRejectedError) Error() string { return e.Reason }
