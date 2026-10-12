package brands

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

type fakeI18n struct{}

func (fakeI18n) T(s string) string               { return s }
func (fakeI18n) Ts(s string, _ ...string) string { return s }

// The postal address renders one line per registry entry, each line kept whole
// (a ZIP+4 such as 33702-4399 must not break at its hyphen), in the public page
// footer and in the footer of the mail a brand sends.
func TestPostalAddressInFooters(t *testing.T) {
	b := valid(t).ByHost("news.machdiamondjewelry.com")
	b.PostalAddress = []string{"7901 4TH ST N STE 300", "ST PETERSBURG FL 33702-4399", "USA"}
	funcs := template.FuncMap{"L": func() fakeI18n { return fakeI18n{} }}

	page := template.Must(template.New("").Funcs(funcs).ParseFiles("../../static/public/templates/index.html"))
	var out bytes.Buffer
	data := map[string]any{"Brand": b, "EnablePublicArchive": true, "RootURL": b.RootURL(), "SourceURL": "https://example.com/src"}
	if err := page.ExecuteTemplate(&out, "footer", data); err != nil {
		t.Fatal(err)
	}
	want := `<span class="nowrap">7901 4TH ST N STE 300</span>, <span class="nowrap">ST PETERSBURG FL 33702-4399</span>, <span class="nowrap">USA</span>`
	if !strings.Contains(out.String(), want) {
		t.Errorf("public footer has no unbreakable address lines:\n%s", out.String())
	}

	mail := template.Must(template.New("").Funcs(funcs).ParseFiles("../../static/email-templates/brand.html"))
	out.Reset()
	if err := mail.ExecuteTemplate(&out, "brand-footer", b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `<span style="white-space:nowrap;">ST PETERSBURG FL 33702-4399</span>, <span style="white-space:nowrap;">USA</span>`) {
		t.Errorf("mail footer has no unbreakable address lines:\n%s", out.String())
	}
}
