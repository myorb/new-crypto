package payouts

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"templ-app/internal/money"
	"templ-app/internal/store"
)

// AssetTotal is completed payout volume in one asset.
type AssetTotal struct {
	AssetID int16
	Count   int64
	Amount  decimal.Decimal
	Fees    decimal.Decimal
}

// CompletedByAsset sums confirmed withdrawals per asset completed since t.
func (s *Service) CompletedByAsset(ctx context.Context, orgID uuid.UUID, since time.Time) ([]AssetTotal, error) {
	rows, err := s.q.SumCompletedWithdrawalsByAsset(ctx, store.SumCompletedWithdrawalsByAssetParams{OrganizationID: orgID, CompletedAt: &since})
	if err != nil {
		return nil, err
	}
	out := make([]AssetTotal, len(rows))
	for i, r := range rows {
		out[i] = AssetTotal{AssetID: r.AssetID, Count: r.Count, Amount: money.FromNumeric(r.Amount), Fees: money.FromNumeric(r.Fees)}
	}
	return out, nil
}
