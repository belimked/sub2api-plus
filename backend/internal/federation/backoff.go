package federation

import "time"

// RetryBackoff grows quadratically with attempt count, capped at 5 minutes.
// Shared by the pusher and the usage tailer so their retry behavior matches.
func RetryBackoff(attempts int) time.Duration {
	backoff := time.Duration(attempts*attempts) * time.Second
	if backoff > 5*time.Minute {
		return 5 * time.Minute
	}
	if backoff < time.Second {
		return time.Second
	}
	return backoff
}

// Truncate keeps error/reason strings from growing unbounded in a text column.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
