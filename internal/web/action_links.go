package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"templ-app/internal/checkout"
	"templ-app/internal/store"
)

const linksPath = "/dashboard/links"

// linkExpiry maps the dialog's expiry choice to a moment in time.
func linkExpiry(choice string, now time.Time) *time.Time {
	var d time.Duration
	switch choice {
	case "24h":
		d = 24 * time.Hour
	case "7d":
		d = 7 * 24 * time.Hour
	case "30d":
		d = 30 * 24 * time.Hour
	default:
		return nil
	}
	t := now.Add(d)
	return &t
}

// createLink handles the "Create payment link" dialog.
func (s *Server) createLink(w http.ResponseWriter, r *http.Request, v *viewer) {
	f := r.PostForm
	in := checkout.LinkInput{
		OrganizationID: v.orgID(),
		Name:           f.Get("name"),
		Description:    optional(f.Get("description")),
		PriceCurrency:  strings.ToUpper(strings.TrimSpace(f.Get("currency"))),
		CollectEmail:   f.Get("collect_email") != "",
		ExpiresAt:      linkExpiry(f.Get("expires"), time.Now()),
		CreatedBy:      uuid.NullUUID{UUID: v.User.ID, Valid: true},
	}
	if in.PriceCurrency == "" {
		in.PriceCurrency = v.Org.Organization.DefaultCurrency
	}
	if f.Get("reusable") == "" {
		once := int32(1)
		in.MaxUses = &once
	}
	// A fixed amount, unless the payer is asked to choose one.
	if f.Get("any_amount") == "" {
		raw := strings.TrimSpace(f.Get("amount"))
		if raw == "" {
			redirectErr(w, r, linksPath, "Enter an amount, or let the customer choose one.", nil)
			return
		}
		amount, err := decimal.NewFromString(raw)
		if err != nil || !amount.IsPositive() {
			redirectErr(w, r, linksPath, "That amount is not valid.", nil)
			return
		}
		in.PriceAmount = &amount
	}
	for _, raw := range f["assets"] {
		id, err := strconv.ParseInt(raw, 10, 16)
		if err != nil {
			continue
		}
		in.AssetIDs = append(in.AssetIDs, int16(id))
	}

	link, err := s.app.Checkout.CreateLink(r.Context(), in)
	if err != nil {
		switch {
		case errors.Is(err, checkout.ErrLinkName):
			redirectErr(w, r, linksPath, "Give the link a name.", nil)
		case errors.Is(err, checkout.ErrLinkPrice):
			redirectErr(w, r, linksPath, "Set a fixed amount or an open one, not both.", nil)
		case errors.Is(err, checkout.ErrBadPrice):
			redirectErr(w, r, linksPath, "That amount is not valid.", nil)
		case errors.Is(err, checkout.ErrBadCurrency):
			redirectErr(w, r, linksPath, "Pick a currency from the list.", nil)
		case errors.Is(err, checkout.ErrLinkSlugTaken):
			redirectErr(w, r, linksPath, "That link address is taken, try again.", nil)
		default:
			s.fail(w, r, err)
		}
		return
	}
	redirectNotice(w, r, linksPath, link.Name+" is live at /l/"+link.Slug)
}

// linkStatus pauses, resumes or archives a link from the row menu.
func (s *Server) linkStatus(w http.ResponseWriter, r *http.Request, v *viewer) {
	id, err := uuid.Parse(r.PostForm.Get("id"))
	if err != nil {
		redirectErr(w, r, linksPath, "That link no longer exists.", nil)
		return
	}
	var status store.PaymentLinkStatus
	switch r.PostForm.Get("status") {
	case "active":
		status = store.PaymentLinkStatusActive
	case "paused":
		status = store.PaymentLinkStatusPaused
	case "archived":
		status = store.PaymentLinkStatusArchived
	default:
		redirectErr(w, r, linksPath, "That is not a status a link can take.", nil)
		return
	}
	link, err := s.app.Checkout.SetLinkStatus(r.Context(), v.orgID(), id, status)
	if err != nil {
		if errors.Is(err, checkout.ErrLinkNotFound) {
			redirectErr(w, r, linksPath, "That link no longer exists.", nil)
			return
		}
		s.fail(w, r, err)
		return
	}
	switch status {
	case store.PaymentLinkStatusActive:
		redirectNotice(w, r, linksPath, link.Name+" accepts payments again.")
	case store.PaymentLinkStatusPaused:
		redirectNotice(w, r, linksPath, link.Name+" is paused. The page still opens but refuses payment.")
	default:
		redirectNotice(w, r, linksPath, link.Name+" was archived.")
	}
}
