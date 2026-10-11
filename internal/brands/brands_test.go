package brands

import (
	"strings"
	"testing"
)

const colors = `{"page":"#f3f1ec","panel":"#ffffff","text":"#1c1917","muted":"#78716c","header":"#1c1917",
	"headerText":"#ffffff","accent":"#af8d36","accentText":"#ffffff","link":"#035f92"}`

func doc(brands string) string {
	return `{
		"domains": [
			{"name": "machdiamondjewelry.com", "brand": "MachDiamond Jewelry"},
			{"name": "firstbankofcardano.com", "brand": "Bank of Cardano"}
		],
		"accounts": [
			{"address": "socials@machdiamondjewelry.com", "aliases": []},
			{"address": "postmaster@machdiamond.tech", "aliases": ["abuse@firstbankofcardano.com"]}
		],
		"marketing": {
			"adminHost": "news.machdiamondjewelry.com",
			"sourceUrl": "https://github.com/mach-diamond/listmonk",
			"guard": {"minSent": 100, "maxBounceRate": 0.02, "maxComplaintRate": 0.0008, "stages": [100, 500], "stageHoldMinutes": 30},
			"brands": [` + brands + `]
		}
	}`
}

func brand(domain, host, from, replyTo string) string {
	return `{"domain": "` + domain + `", "host": "` + host + `", "configurationSet": "marketing-x",
		"from": "` + from + `", "replyTo": "` + replyTo + `", "site": "https://` + domain + `",
		"postalAddress": "", "font": "'Inter', -apple-system, sans-serif", "colors": ` + colors + `}`
}

func valid(t *testing.T) *Registry {
	t.Helper()
	r, err := Parse([]byte(doc(
		brand("machdiamondjewelry.com", "news.machdiamondjewelry.com", "hello@news.machdiamondjewelry.com", "socials@machdiamondjewelry.com") + "," +
			brand("firstbankofcardano.com", "news.firstbankofcardano.com", "hello@news.firstbankofcardano.com", "abuse@firstbankofcardano.com"))))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLookups(t *testing.T) {
	r := valid(t)

	for host, want := range map[string]string{
		"news.machdiamondjewelry.com":                "machdiamondjewelry.com",
		"NEWS.MachDiamondJewelry.com:443":            "machdiamondjewelry.com",
		"news.firstbankofcardano.com.localhost:9000": "firstbankofcardano.com",
		"machdiamondjewelry.com":                     "",
		"news.example.com":                           "",
		"":                                           "",
	} {
		got := ""
		if b := r.ByHost(host); b != nil {
			got = b.Domain
		}
		if got != want {
			t.Errorf("ByHost(%q) = %q, want %q", host, got, want)
		}
	}

	for from, want := range map[string]string{
		"MachDiamond Jewelry <hello@news.machdiamondjewelry.com>": "machdiamondjewelry.com",
		"hello@NEWS.firstbankofcardano.com":                       "firstbankofcardano.com",
		"support@machdiamondjewelry.com":                          "",
		"garbage":                                                 "",
	} {
		got := ""
		if b := r.ByAddress(from); b != nil {
			got = b.Domain
		}
		if got != want {
			t.Errorf("ByAddress(%q) = %q, want %q", from, got, want)
		}
	}

	for tags, want := range map[string]string{
		"brand:machdiamondjewelry.com":      "machdiamondjewelry.com",
		"news,brand:firstbankofcardano.com": "firstbankofcardano.com",
		"news":                              "",
		"brand:example.com":                 "",
		"brand:machdiamondjewelry.com,brand:firstbankofcardano.com": "",
		"brand:machdiamondjewelry.com,brand:machdiamondjewelry.com": "machdiamondjewelry.com",
	} {
		got := ""
		if b := r.ByListTags(strings.Split(tags, ",")); b != nil {
			got = b.Domain
		}
		if got != want {
			t.Errorf("ByListTags(%q) = %q, want %q", tags, got, want)
		}
	}
}

func TestBrandValues(t *testing.T) {
	b := valid(t).ByHost("news.machdiamondjewelry.com")
	if b.Name != "MachDiamond Jewelry" {
		t.Errorf("Name = %q", b.Name)
	}
	if got := b.FromHeader(); got != "MachDiamond Jewelry <hello@news.machdiamondjewelry.com>" {
		t.Errorf("FromHeader = %q", got)
	}
	if b.RootURL() != "https://news.machdiamondjewelry.com" || b.ListTag() != "brand:machdiamondjewelry.com" {
		t.Errorf("RootURL/ListTag = %q %q", b.RootURL(), b.ListTag())
	}
	if b.HasPostalAddress() {
		t.Error("an empty postal address must not count")
	}
}

func TestRefusals(t *testing.T) {
	jewelry := func(host, from, replyTo string) string {
		return brand("machdiamondjewelry.com", host, from, replyTo)
	}
	cases := map[string]string{
		"unknown domain":     brand("example.com", "news.example.com", "a@news.example.com", "socials@machdiamondjewelry.com"),
		"host off domain":    jewelry("news.example.com", "hello@news.example.com", "socials@machdiamondjewelry.com"),
		"from off host":      jewelry("news.machdiamondjewelry.com", "hello@machdiamondjewelry.com", "socials@machdiamondjewelry.com"),
		"unknown reply-to":   jewelry("news.machdiamondjewelry.com", "hello@news.machdiamondjewelry.com", "nobody@machdiamondjewelry.com"),
		"bad colour":         strings.Replace(jewelry("news.machdiamondjewelry.com", "hello@news.machdiamondjewelry.com", "socials@machdiamondjewelry.com"), "#035f92", "blue", 1),
		"admin host missing": brand("firstbankofcardano.com", "news.firstbankofcardano.com", "hello@news.firstbankofcardano.com", "abuse@firstbankofcardano.com"),
	}
	for name, b := range cases {
		if _, err := Parse([]byte(doc(b))); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}

	bad := strings.Replace(doc(jewelryOK()), `"stages": [100, 500]`, `"stages": [500, 100]`, 1)
	if _, err := Parse([]byte(bad)); err == nil {
		t.Error("decreasing stages: expected a refusal")
	}
}

func jewelryOK() string {
	return brand("machdiamondjewelry.com", "news.machdiamondjewelry.com", "hello@news.machdiamondjewelry.com", "socials@machdiamondjewelry.com")
}

func TestGuard(t *testing.T) {
	g := valid(t).Guard

	for _, c := range []struct {
		sent, hard, complaints int
		pause                  bool
	}{
		{99, 50, 50, false}, // too few to judge
		{100, 2, 0, false},  // exactly 2% is allowed
		{100, 3, 0, true},
		{1000, 20, 0, false},
		{1000, 21, 0, true},
		{100, 0, 1, true}, // 1% complaints
		{2500, 0, 2, false},
		{2500, 0, 3, true}, // 0.12%
	} {
		if got := g.PauseReason(c.sent, c.hard, c.complaints) != ""; got != c.pause {
			t.Errorf("PauseReason(%d, %d, %d) pause = %v, want %v", c.sent, c.hard, c.complaints, got, c.pause)
		}
	}

	for queued, want := range map[int]int{0: 100, 99: 100, 100: 500, 499: 500, 500: 0, 9000: 0} {
		if got := g.NextStage(queued); got != want {
			t.Errorf("NextStage(%d) = %d, want %d", queued, got, want)
		}
	}
}
