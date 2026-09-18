package checkout

import (
	"context"
	"time"

	"github.com/google/uuid"

	"templ-app/internal/store"
)

// CountSince counts invoices created since t.
func (s *Service) CountSince(ctx context.Context, orgID uuid.UUID, since time.Time) (int64, error) {
	return s.q.CountInvoicesSince(ctx, store.CountInvoicesSinceParams{OrganizationID: orgID, CreatedAt: since})
}
