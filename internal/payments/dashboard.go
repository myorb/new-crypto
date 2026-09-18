package payments

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"templ-app/internal/money"
	"templ-app/internal/store"
)

// DailyVolume is settled volume of one asset on one day.
type DailyVolume struct {
	Day     time.Time
	AssetID int16
	Count   int64
	Amount  decimal.Decimal
}

// VolumeByDay lists settled (confirmed or credited) payments per day and
// asset since t.
func (s *Service) VolumeByDay(ctx context.Context, orgID uuid.UUID, since time.Time) ([]DailyVolume, error) {
	rows, err := s.q.SumPaymentsByDay(ctx, store.SumPaymentsByDayParams{OrganizationID: orgID, CreatedAt: since})
	if err != nil {
		return nil, err
	}
	out := make([]DailyVolume, len(rows))
	for i, r := range rows {
		out[i] = DailyVolume{Day: r.Day.Time, AssetID: r.AssetID, Count: r.Count, Amount: money.FromNumeric(r.Amount)}
	}
	return out, nil
}

// DailyStatus is the number of payments in one status on one day.
type DailyStatus struct {
	Day    time.Time
	Status store.PaymentStatus
	Count  int64
}

// StatusByDay counts payments per day and status since t.
func (s *Service) StatusByDay(ctx context.Context, orgID uuid.UUID, since time.Time) ([]DailyStatus, error) {
	rows, err := s.q.CountPaymentsByDayAndStatus(ctx, store.CountPaymentsByDayAndStatusParams{OrganizationID: orgID, CreatedAt: since})
	if err != nil {
		return nil, err
	}
	out := make([]DailyStatus, len(rows))
	for i, r := range rows {
		out[i] = DailyStatus{Day: r.Day.Time, Status: r.Status, Count: r.Count}
	}
	return out, nil
}

// AssetVolume is settled volume of one asset over a period.
type AssetVolume struct {
	AssetID   int16
	AssetCode string
	Symbol    string
	Name      string
	Count     int64
	Amount    decimal.Decimal
	Fees      decimal.Decimal
}

// ByAsset sums settled payments per asset since t, largest first.
func (s *Service) ByAsset(ctx context.Context, orgID uuid.UUID, since time.Time) ([]AssetVolume, error) {
	rows, err := s.q.SumPaymentsByAsset(ctx, store.SumPaymentsByAssetParams{OrganizationID: orgID, CreatedAt: since})
	if err != nil {
		return nil, err
	}
	out := make([]AssetVolume, len(rows))
	for i, r := range rows {
		out[i] = AssetVolume{AssetID: r.AssetID, AssetCode: r.AssetCode, Symbol: r.Symbol, Name: r.AssetName, Count: r.Count, Amount: money.FromNumeric(r.Amount), Fees: money.FromNumeric(r.Fees)}
	}
	return out, nil
}

// NetworkCount is the number of payments seen on one network.
type NetworkCount struct {
	Code  string
	Name  string
	Count int64
}

// ByNetwork counts payments per network since t, busiest first.
func (s *Service) ByNetwork(ctx context.Context, orgID uuid.UUID, since time.Time) ([]NetworkCount, error) {
	rows, err := s.q.CountPaymentsByNetwork(ctx, store.CountPaymentsByNetworkParams{OrganizationID: orgID, CreatedAt: since})
	if err != nil {
		return nil, err
	}
	out := make([]NetworkCount, len(rows))
	for i, r := range rows {
		out[i] = NetworkCount{Code: r.NetworkCode, Name: r.NetworkName, Count: r.Count}
	}
	return out, nil
}

// CountSince counts all payments (any status) since t.
func (s *Service) CountSince(ctx context.Context, orgID uuid.UUID, since time.Time) (int64, error) {
	return s.q.CountPaymentsSince(ctx, store.CountPaymentsSinceParams{OrganizationID: orgID, CreatedAt: since})
}
