package core

import "errors"

// ErrNativeSessionUnavailable marks a persisted, agent-native resume handle
// that the backing runtime has permanently discarded. The Engine can safely
// start a fresh session in this case, unlike authentication, transport, or
// other resume failures which must still be returned to the caller.
var ErrNativeSessionUnavailable = errors.New("native session is no longer available")
