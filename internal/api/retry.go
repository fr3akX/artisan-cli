package api

import (
	"context"
	"net/http"
	"time"
)

const maxAttempts = 3

var retryBackoff = [...]time.Duration{10 * time.Millisecond, 20 * time.Millisecond}

func isSafeRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func isTransientStatus(status int) bool {
	return status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

func requestCanRetry(request Request) bool {
	if isSafeRead(request.Method) {
		return true
	}
	return request.Body != nil && request.IdempotencyKey != ""
}

func waitForRetry(ctx context.Context, completedAttempt int) error {
	delay := retryBackoff[completedAttempt]
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
