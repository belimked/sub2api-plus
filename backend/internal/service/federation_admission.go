package service

import (
	"context"

	dbent "github.com/LuckyKuang/sub2api-plus/ent"
	"github.com/LuckyKuang/sub2api-plus/ent/usagelog"
)

// FederationAvailableBalance computes available balance under the
// mainland/overseas water-mark scheme: balance minus the cost of local
// usage_log rows not yet reflected in that balance (id > watermarkSeq). See
// sub2api-federation-design.md §5.3 and
// openspec/changes/federation-admission-check/design.md.
//
// Only wired in (see cmd/server/main.go) when federation.admission_check_enabled
// is true; every other deployment never calls this.
func FederationAvailableBalance(ctx context.Context, client *dbent.Client, userID int64, balance float64, watermarkSeq int64) (float64, error) {
	var rows []struct {
		Sum float64 `json:"sum"`
	}
	err := client.UsageLog.Query().
		Where(usagelog.UserIDEQ(userID), usagelog.IDGT(watermarkSeq)).
		Aggregate(dbent.As(dbent.Sum(usagelog.FieldActualCost), "sum")).
		Scan(ctx, &rows)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return balance, nil
	}
	return balance - rows[0].Sum, nil
}
