package manager

import (
	"log"
	"os"
	"strings"
	"testing"

	"github.com/knadh/listmonk/internal/brands"
	"github.com/knadh/listmonk/models"
)

const testRegistry = `{
	"domains": [{"name": "machdiamondjewelry.com", "brand": "MachDiamond Jewelry"}],
	"accounts": [{"address": "socials@machdiamondjewelry.com", "aliases": []}],
	"marketing": {
		"adminHost": "news.machdiamondjewelry.com",
		"sourceUrl": "https://github.com/mach-diamond/listmonk",
		"guard": {"minSent": 100, "maxBounceRate": 0.02, "maxComplaintRate": 0.0008, "stages": [100], "stageHoldMinutes": 30},
		"brands": [{"domain": "machdiamondjewelry.com", "host": "news.machdiamondjewelry.com", "configurationSet": "marketing-x",
			"from": "hello@news.machdiamondjewelry.com", "replyTo": "socials@machdiamondjewelry.com", "site": "https://machdiamondjewelry.com",
			"postalAddress": [], "font": "sans-serif", "colors": {"page": "#ffffff", "panel": "#ffffff", "text": "#000000", "muted": "#555555",
			"header": "#000000", "headerText": "#ffffff", "accent": "#000000", "accentText": "#ffffff", "link": "#0000ff"}}]
	}
}`

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	r, err := brands.Parse([]byte(testRegistry))
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{URLs: NewSiteURLs("https://news.admin.example"), Brands: r}, nil, nil, log.New(os.Stderr, "", 0))
}

func TestSiteURLsFollowTheBrand(t *testing.T) {
	m := newTestManager(t)

	branded := &models.Campaign{FromEmail: "MachDiamond Jewelry <hello@news.machdiamondjewelry.com>"}
	if got := m.SiteURLs(branded).UnsubURL; !strings.HasPrefix(got, "https://news.machdiamondjewelry.com/subscription/") {
		t.Errorf("branded unsubscribe URL = %q", got)
	}

	unbranded := &models.Campaign{FromEmail: "someone@example.com"}
	if got := m.SiteURLs(unbranded).RootURL; got != "https://news.admin.example" {
		t.Errorf("unbranded root = %q", got)
	}

	// Template validation compiles with no campaign at all.
	if m.Brand(nil) != nil || m.SiteURLs(nil).RootURL != "https://news.admin.example" {
		t.Error("a nil campaign must fall back to the admin host")
	}
	if f := m.TemplateFuncs(nil); f["UnsubscribeURL"] == nil {
		t.Error("TemplateFuncs(nil) lost its functions")
	}
}
