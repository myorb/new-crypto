package pages

import (
	"templ-app/blocks/dashboard"
	"templ-app/layouts"
)

// shell builds the sidebar props every dashboard page shares: the current
// organization, the organizations the user can switch to, and the signed-in
// user. Replace the fixtures with session data in a handler.
func shell(title, activePath string, sandbox bool) layouts.DashboardProps {
	org := dashboard.Fixture(sandbox)
	acct := dashboard.AccountFixture(sandbox)
	orgs := make([]layouts.Org, len(acct.Orgs))
	for i, o := range acct.Orgs {
		orgs[i] = layouts.Org{Name: o.Name, Plan: o.Plan, Initials: o.Initials, Href: "/dashboard", Current: o.Current}
	}
	return layouts.DashboardProps{
		Title:      title,
		Subtitle:   org.Today,
		ActivePath: activePath,
		Merchant:   org.MerchantName,
		Initials:   org.Initials,
		Plan:       "Business plan",
		Orgs:       orgs,
		User: layouts.User{
			Name:     acct.User.Name,
			Email:    acct.User.Email,
			Initials: acct.User.Initials,
			Role:     acct.User.Role,
		},
		Sandbox: sandbox,
	}
}
