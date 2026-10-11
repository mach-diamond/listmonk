package manager

import (
	"fmt"
	"net/textproto"

	"github.com/knadh/listmonk/internal/brands"
	"github.com/knadh/listmonk/models"
)

// HeaderSESConfigurationSet names the SES configuration set a message is sent
// under when it goes through the SES SMTP interface.
const HeaderSESConfigurationSet = "X-SES-CONFIGURATION-SET"

// SiteURLs are the public link formats under one site's root URL. Every
// brand has its own set, so a campaign's unsubscribe, tracking, web view and
// archive links point at the brand's news host and not at the admin host.
type SiteURLs struct {
	RootURL string

	// url.com/subscription/{campaign_uuid}/{subscriber_uuid}
	UnsubURL string

	// url.com/subscription/optin/{subscriber_uuid}?{list query}
	OptinURL string

	// url.com/link/{link_uuid}/{campaign_uuid}/{subscriber_uuid}
	LinkTrackURL string

	// url.com/campaign/{campaign_uuid}/{subscriber_uuid}/px.png
	ViewTrackURL string

	// url.com/campaign/{campaign_uuid}/{subscriber_uuid}
	MessageURL string

	// url.com/archive
	ArchiveURL string
}

// NewSiteURLs builds the link formats for a root URL without a trailing slash.
func NewSiteURLs(root string) SiteURLs {
	return SiteURLs{
		RootURL:      root,
		UnsubURL:     fmt.Sprintf("%s/subscription/%%s/%%s", root),
		OptinURL:     fmt.Sprintf("%s/subscription/optin/%%s?%%s", root),
		LinkTrackURL: fmt.Sprintf("%s/link/%%s/%%s/%%s", root),
		ViewTrackURL: fmt.Sprintf("%s/campaign/%%s/%%s/px.png", root),
		MessageURL:   fmt.Sprintf("%s/campaign/%%s/%%s", root),
		ArchiveURL:   root + "/archive",
	}
}

// Brand returns the brand a campaign sends as (by its From address), or nil.
func (m *Manager) Brand(c *models.Campaign) *brands.Brand {
	return m.cfg.Brands.ByAddress(c.FromEmail)
}

// SiteURLs returns the link formats for a campaign: its brand's, or the admin
// host's for a campaign that has no brand (a draft; such a campaign cannot start).
func (m *Manager) SiteURLs(c *models.Campaign) SiteURLs {
	return m.BrandURLs(m.Brand(c))
}

// BrandURLs returns a brand's link formats, or the admin host's for nil.
func (m *Manager) BrandURLs(b *brands.Brand) SiteURLs {
	if u, ok := m.brandURLs[b]; ok {
		return u
	}

	return m.cfg.URLs
}

// SetBrandHeaders adds the headers every message a brand sends carries: its
// Reply-To and the SES configuration set that publishes its bounce and
// complaint events and keeps its sending reputation apart from transactional mail.
func SetBrandHeaders(h textproto.MIMEHeader, b *brands.Brand) {
	h.Set("Reply-To", b.ReplyTo)
	h.Set(HeaderSESConfigurationSet, b.ConfigurationSet)
}
