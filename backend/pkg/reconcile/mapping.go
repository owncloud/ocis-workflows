// Package reconcile backstops oCIS's SSE notification stream, which is live-only and
// silently drops any event that fires while a user's connection is down or reconnecting
// (see sse.Manager's package doc). It queries oCIS's activitylog service — which persists
// event history independently of whether this backend was listening — for anything that
// happened since the last time a given user's drive was checked, and dispatches matching
// workflows the same way sse.Manager.handleEvent does for live events.
package reconcile

// messageToTriggerType maps oCIS activitylog's fixed, untranslated message-template
// constants (verified against oCIS source: services/activitylog/pkg/service/response.go's
// NewActivity never translates the message itself) to this backend's own trigger-type
// vocabulary. Only upload/move are covered in this iteration — share and lock triggers stay
// SSE-only (lock has no activitylog message at all to map from).
var messageToTriggerType = map[string]string{
	"{user} added {resource} to {folder}":       "upload",
	"{user} moved {resource} to {folder}":       "move",
	"{user} renamed {oldResource} to {resource}": "move",
}

// TriggerType returns the trigger type an activitylog message maps to, and whether it's
// one this backstop handles at all — any other message (share, trash, unrecognized future
// additions) is deliberately ignored, not an error.
func TriggerType(message string) (string, bool) {
	t, ok := messageToTriggerType[message]
	return t, ok
}
