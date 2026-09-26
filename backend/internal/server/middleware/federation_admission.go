package middleware

import (
	"context"
	"log/slog"
)

// FederationAvailableBalanceFunc computes available balance under the
// mainland/overseas water-mark federation scheme (balance minus not-yet-
// reflected local usage). nil by default -- every existing deployment gets
// exactly the pre-federation behavior. Set once at startup (see
// cmd/server/main.go) only when cfg.Federation.AdmissionCheckEnabled is
// true. See sub2api-federation-design.md and
// openspec/changes/federation-admission-check/.
var FederationAvailableBalanceFunc func(ctx context.Context, userID int64, balance float64, watermarkSeq int64) (available float64, err error)

// federationAvailableBalance evaluates the hook when set, failing open to
// the plain balance on any error -- a query hiccup on the federation side
// must not block all traffic for a user who otherwise has funds.
func federationAvailableBalance(ctx context.Context, userID int64, balance float64, watermarkSeq int64) float64 {
	if FederationAvailableBalanceFunc == nil {
		return balance
	}
	available, err := FederationAvailableBalanceFunc(ctx, userID, balance, watermarkSeq)
	if err != nil {
		slog.Error("federation admission check failed, falling back to plain balance", "user_id", userID, "error", err)
		return balance
	}
	return available
}
