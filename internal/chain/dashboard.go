package chain

import (
	"context"

	"github.com/google/uuid"

	"templ-app/internal/store"
)

// ActivityRow is one transfer on a merchant address with its transaction
// and asset labels.
type ActivityRow = store.ListOrganizationTransfersRow

// OrganizationActivity lists transfers touching a merchant's addresses,
// newest first.
func (s *Service) OrganizationActivity(ctx context.Context, orgID uuid.UUID, limit, offset int32) ([]ActivityRow, error) {
	return s.q.ListOrganizationTransfers(ctx, store.ListOrganizationTransfersParams{OrganizationID: uuid.NullUUID{UUID: orgID, Valid: true}, Limit: limit, Offset: offset})
}

// CountOrganizationActivity counts a merchant's transfers.
func (s *Service) CountOrganizationActivity(ctx context.Context, orgID uuid.UUID) (int64, error) {
	return s.q.CountOrganizationTransfers(ctx, uuid.NullUUID{UUID: orgID, Valid: true})
}

// CountOrganizationAddresses counts a merchant's deposit addresses.
func (s *Service) CountOrganizationAddresses(ctx context.Context, orgID uuid.UUID) (int64, error) {
	return s.q.CountOrganizationAddresses(ctx, uuid.NullUUID{UUID: orgID, Valid: true})
}
