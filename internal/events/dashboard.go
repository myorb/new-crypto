package events

import (
	"context"
	"time"

	"github.com/google/uuid"

	"templ-app/internal/store"
)

// DeliveryRow is one delivery with its event type and endpoint URL.
type DeliveryRow = store.ListWebhookDeliveriesForOrganizationRow

// OrganizationDeliveries lists a merchant's deliveries across endpoints.
func (s *Service) OrganizationDeliveries(ctx context.Context, orgID uuid.UUID, limit, offset int32) ([]DeliveryRow, error) {
	return s.q.ListWebhookDeliveriesForOrganization(ctx, store.ListWebhookDeliveriesForOrganizationParams{OrganizationID: orgID, Limit: limit, Offset: offset})
}

// DailyDeliveries is the number of deliveries in one status on one day.
type DailyDeliveries struct {
	Day    time.Time
	Status store.WebhookDeliveryStatus
	Count  int64
}

// DeliveriesByDay counts deliveries per day and status since t.
func (s *Service) DeliveriesByDay(ctx context.Context, orgID uuid.UUID, since time.Time) ([]DailyDeliveries, error) {
	rows, err := s.q.CountWebhookDeliveriesByDay(ctx, store.CountWebhookDeliveriesByDayParams{OrganizationID: orgID, CreatedAt: since})
	if err != nil {
		return nil, err
	}
	out := make([]DailyDeliveries, len(rows))
	for i, r := range rows {
		out[i] = DailyDeliveries{Day: r.Day.Time, Status: r.Status, Count: r.Count}
	}
	return out, nil
}

// EndpointStats summarises an endpoint's delivery history.
type EndpointStats struct {
	Total, Succeeded, Failed int64
	LastAttempt              *time.Time
}

// SuccessRate is succeeded / total in percent; 100 when nothing was sent.
func (e EndpointStats) SuccessRate() float64 {
	if e.Total == 0 {
		return 100
	}
	return float64(e.Succeeded) / float64(e.Total) * 100
}

// Stats returns an endpoint's delivery statistics.
func (s *Service) Stats(ctx context.Context, endpointID uuid.UUID) (EndpointStats, error) {
	r, err := s.q.GetEndpointDeliveryStats(ctx, endpointID)
	if err != nil {
		return EndpointStats{}, err
	}
	st := EndpointStats{Total: r.Total, Succeeded: r.Succeeded, Failed: r.Failed}
	if r.LastAttemptAt.Unix() > 0 {
		t := r.LastAttemptAt
		st.LastAttempt = &t
	}
	return st, nil
}

// Types lists the event types the platform emits, for subscription pickers.
func Types() []string {
	return []string{
		InvoiceCreated, InvoicePartial, InvoicePaid, InvoiceConfirmed, InvoiceCompleted, InvoiceExpired, InvoiceCancelled,
		PaymentLinkCreated, PaymentLinkUpdated,
		CustomerCreated, CustomerBlocked,
		PaymentDetected, PaymentConfirmed, PaymentCredited, PaymentReverted,
		WithdrawalRequested, WithdrawalApproved, WithdrawalRejected, WithdrawalCancelled, WithdrawalBroadcast, WithdrawalCompleted, WithdrawalFailed,
	}
}
