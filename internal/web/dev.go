package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"

	"github.com/shopspring/decimal"

	"templ-app/internal/chain"
	"templ-app/internal/chain/devchain"
	"templ-app/internal/checkout"
	"templ-app/internal/store"
)

var devCustomers = []string{"billing@acme-retail.de", "lena.h@proton.me", "ap@orbitmedia.co", "kenji@wtnb.jp", "finance@bloomco.io", "sofia.almeida@gmail.com", "pay@helixgames.gg", "invoices@nordicsupply.se"}

// devSimulate creates n invoices and pays them through the real flow
// (invoice, option, address, observed transfer, credit) so the dashboard has
// data. Development only; mounted when GO_ENV is not production.
func (s *Server) devSimulate(w http.ResponseWriter, r *http.Request, v *viewer) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n < 1 {
		n = 5
	}
	if n > 50 {
		n = 50
	}
	ctx := r.Context()
	assets, err := s.app.Catalog.Assets(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	created, skipped := 0, 0
	var lastErr error
	for i := 0; i < n; i++ {
		a := assets[rand.IntN(len(assets))]
		price := decimal.NewFromFloat(float64(rand.IntN(88000)+1500) / 100)
		email := devCustomers[rand.IntN(len(devCustomers))]
		desc := "Order #" + strconv.Itoa(1000+rand.IntN(9000))
		inv, err := s.app.Checkout.Create(ctx, checkout.CreateInput{
			OrganizationID: v.orgID(), PriceCurrency: v.Org.Organization.DefaultCurrency, PriceAmount: price,
			Description: &desc, CustomerEmail: &email, AssetIDs: []int16{a.ID},
		})
		if err != nil {
			skipped++
			lastErr = err
			continue
		}
		inv, err = s.app.Checkout.SelectOption(ctx, inv.ID, a.ID)
		if err != nil {
			skipped++
			lastErr = err
			continue
		}
		sel := inv.Selected()
		if sel == nil || sel.Address == nil {
			skipped++
			continue
		}
		seed := sha256.Sum256([]byte(fmt.Sprintf("payer-%s-%d", inv.ID, i)))
		payer := devchain.Format(a.Network.Family, seed[:])
		amount := decimal.NewFromBigInt(sel.AmountDue.Int, sel.AmountDue.Exp)
		status, confirmations := store.TxStatusConfirmed, a.Network.RequiredConfirmations
		switch roll := rand.IntN(10); {
		case roll == 0: // still pending
			status, confirmations = store.TxStatusPending, 0
		case roll == 1: // slight overpayment, confirmed
			amount = amount.Mul(decimal.NewFromFloat(1.01))
		}
		hash := sha256.Sum256([]byte("tx-" + inv.ID.String()))
		txHash := hex.EncodeToString(hash[:])
		if a.Network.Family == store.NetworkFamilyEvm {
			txHash = "0x" + txHash
		} else if a.Network.Family == store.NetworkFamilySolana {
			txHash = devchain.Format(store.NetworkFamilySolana, hash[:]) + devchain.Format(store.NetworkFamilySolana, seed[:])[:24]
		}
		_, err = s.app.Chain.RecordTransaction(ctx, chain.ObservedTransaction{
			NetworkID: a.NetworkID, ProviderID: sel.ProviderID, Hash: txHash, Status: status, Confirmations: confirmations,
			From: &payer, To: sel.Address,
			Transfers: []chain.ObservedTransfer{{AssetID: a.ID, LogIndex: 0, From: &payer, To: *sel.Address, Amount: amount}},
		})
		if err != nil {
			skipped++
			lastErr = err
			continue
		}
		created++
	}
	if created == 0 && lastErr != nil {
		s.fail(w, r, errors.Join(errors.New("dev simulate: nothing created"), lastErr))
		return
	}
	s.log.Info("dev simulate", "created", created, "skipped", skipped, "last_error", lastErr)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}
