package themetoggle

import (
	"context"
	"io"

	"github.com/a-h/templ"

	"templ-app/components/dropdownmenu"
)

// triggerAttrs merges the dropdown trigger attributes with caller extras and
// the relative positioning the stacked sun/moon icons need.
func triggerAttrs(ctx context.Context, extra templ.Attributes) templ.Attributes {
	attrs := templ.Attributes{"aria-label": "Toggle theme", "data-tui-themetoggle-trigger": true}
	for k, v := range extra {
		attrs[k] = v
	}
	for k, v := range dropdownmenu.Trigger(ctx) {
		attrs[k] = v
	}
	return attrs
}

// initJS runs synchronously in <head> before the body is parsed, so a stored
// dark preference never flashes the light theme. It mirrors the resolve logic
// in themetoggle.js; keep the two in sync.
const initJS = `(function(){try{var t=localStorage.getItem("theme");var d=t==="dark"||((!t||t==="system")&&window.matchMedia("(prefers-color-scheme: dark)").matches);var r=document.documentElement;r.classList.toggle("dark",d);r.style.colorScheme=d?"dark":"light";}catch(e){}})();`

// InitScript renders the blocking theme bootstrap. Place it in <head> right
// after the stylesheet link.
func InitScript() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		nonce := templ.GetNonce(ctx)
		open := "<script"
		if nonce != "" {
			open += ` nonce="` + templ.EscapeString(nonce) + `"`
		}
		_, err := io.WriteString(w, open+">"+initJS+"</script>")
		return err
	})
}
