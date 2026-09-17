// Package auth holds the sign-in, sign-up and two-factor screens. They are
// frontend-only: forms navigate to the next step client-side so the flow
// can be clicked through without a backend.
package auth

import (
	"fmt"
	"strings"
)

// Verify2FA is the state of the second-factor step.
type Verify2FA struct {
	Email  string
	Method string // "app", "sms", "passkey"
	Phone  string // masked, for sms
}

// Setup2FA is the state of the authenticator enrolment step.
type Setup2FA struct {
	Email   string
	Issuer  string
	Secret  string // base32, grouped
	OTPAuth string
}

// VerifyFixture returns the sample verification state.
func VerifyFixture() Verify2FA {
	return Verify2FA{Email: "alex@northwind.dev", Method: "app", Phone: "+49 •••• 4471"}
}

// SetupFixture returns the sample enrolment state.
func SetupFixture() Setup2FA {
	secret := "JBSW Y3DP EHPK 3PXP JBSW Y3DP EHPK 3PXP"
	return Setup2FA{
		Email:   "alex@northwind.dev",
		Issuer:  "PayChain",
		Secret:  secret,
		OTPAuth: "otpauth://totp/PayChain:alex@northwind.dev?secret=" + strings.ReplaceAll(secret, " ", "") + "&issuer=PayChain",
	}
}

// qrModules draws a deterministic QR-looking pattern for the mock: three
// finder squares plus pseudo-random data modules seeded from the payload.
func qrModules(payload string, n int) [][]bool {
	grid := make([][]bool, n)
	for i := range grid {
		grid[i] = make([]bool, n)
	}
	finder := func(ox, oy int) {
		for y := 0; y < 7; y++ {
			for x := 0; x < 7; x++ {
				edge := x == 0 || y == 0 || x == 6 || y == 6
				core := x >= 2 && x <= 4 && y >= 2 && y <= 4
				grid[oy+y][ox+x] = edge || core
			}
		}
	}
	inFinder := func(x, y int) bool {
		return (x < 8 && y < 8) || (x >= n-8 && y < 8) || (x < 8 && y >= n-8)
	}
	var h uint32 = 2166136261
	for i := range payload {
		h ^= uint32(payload[i])
		h *= 16777619
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if inFinder(x, y) {
				continue
			}
			h ^= uint32(x*31 + y*17)
			h *= 16777619
			grid[y][x] = h&0x300 != 0 && h&0x3 != 0
		}
	}
	finder(0, 0)
	finder(n-7, 0)
	finder(0, n-7)
	return grid
}

// qrPath renders the dark modules as one SVG path.
func qrPath(payload string, n int) string {
	var b strings.Builder
	for y, row := range qrModules(payload, n) {
		for x, on := range row {
			if on {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	return b.String()
}
