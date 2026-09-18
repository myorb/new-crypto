package ledger

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"templ-app/internal/money"
	"templ-app/internal/store"
)

func TestStandardPostingsBalance(t *testing.T) {
	id, org := uuid.New(), uuid.New()
	cases := map[string]Posting{
		"payment credited":     PaymentCredited(id, org, 1, money.MustParse("100"), money.MustParse("1.5")),
		"payment no fee":       PaymentCredited(id, org, 1, money.MustParse("100"), money.MustParse("0")),
		"withdrawal requested": WithdrawalRequested(id, org, 1, money.MustParse("50.5")),
		"withdrawal released":  WithdrawalReleased(id, org, 1, money.MustParse("50.5"), "rejected"),
		"withdrawal completed": WithdrawalCompleted(id, org, 1, money.MustParse("50"), money.MustParse("0.5")),
		"withdrawal free":      WithdrawalCompleted(id, org, 1, money.MustParse("50"), money.MustParse("0")),
		"network fee":          NetworkFeePaid(RefWithdrawal, id, 2, money.MustParse("0.001")),
		"rebalance to cold":    Rebalance(id, 1, money.MustParse("1000"), true),
		"rebalance to hot":     Rebalance(id, 1, money.MustParse("1000"), false),
	}
	for name, p := range cases {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateRejectsBadPostings(t *testing.T) {
	org := uuid.NullUUID{UUID: uuid.New(), Valid: true}
	base := Posting{EventType: "x", ReferenceType: RefPayment, ReferenceID: uuid.New()}

	p := base
	p.Lines = []Line{
		{OrganizationID: org, AssetID: 1, Type: store.LedgerAccountTypeMerchantAvailable, Amount: money.MustParse("10")},
		{AssetID: 1, Type: store.LedgerAccountTypePlatformHotWallet, Amount: money.MustParse("-9")},
	}
	if err := p.Validate(); !errors.Is(err, ErrUnbalanced) {
		t.Errorf("unbalanced: %v", err)
	}

	p.Lines[1].Amount = money.MustParse("-10")
	p.Lines[1].AssetID = 2
	if err := p.Validate(); !errors.Is(err, ErrMixedAssets) {
		t.Errorf("mixed assets: %v", err)
	}

	p.Lines[1].AssetID = 1
	p.Lines[1].OrganizationID = org // platform account with an org
	if err := p.Validate(); !errors.Is(err, ErrBadAccountType) {
		t.Errorf("platform with org: %v", err)
	}

	p.Lines[1].OrganizationID = uuid.NullUUID{}
	p.Lines[0].OrganizationID = uuid.NullUUID{} // merchant account without an org
	if err := p.Validate(); !errors.Is(err, ErrBadAccountType) {
		t.Errorf("merchant without org: %v", err)
	}

	p.Lines = p.Lines[:1]
	if err := p.Validate(); !errors.Is(err, ErrEmptyPosting) {
		t.Errorf("single line: %v", err)
	}

	z := base
	z.Lines = []Line{
		{OrganizationID: org, AssetID: 1, Type: store.LedgerAccountTypeMerchantAvailable, Amount: money.MustParse("0")},
		{AssetID: 1, Type: store.LedgerAccountTypePlatformHotWallet, Amount: money.MustParse("0")},
	}
	if err := z.Validate(); !errors.Is(err, ErrZeroLine) {
		t.Errorf("zero line: %v", err)
	}

	noRef := Posting{Lines: z.Lines}
	noRef.Lines[0].Amount, noRef.Lines[1].Amount = money.MustParse("1"), money.MustParse("-1")
	if err := noRef.Validate(); err == nil {
		t.Error("missing reference should fail")
	}
}
