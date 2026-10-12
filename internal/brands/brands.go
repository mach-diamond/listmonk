// Package brands reads the newsletter brands from the mail registry
// (_Shared/Email/packages/mail-registry/registry.json) so one listmonk
// instance can serve several brands. A brand is chosen by the request Host on
// public pages, by the From address for campaigns and by a "brand:<domain>"
// tag on lists.
package brands

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/knadh/listmonk/internal/utils"
)

// ListTagPrefix marks the brand a list belongs to: a list tagged
// "brand:machdiamondjewelry.com" is shown and sent only under that brand.
const ListTagPrefix = "brand:"

// devHostSuffix lets a browser on the operator's machine reach a brand's pages
// at http://news.<domain>.localhost:9000; browsers resolve *.localhost to
// loopback and Caddy never forwards such a name in production.
const devHostSuffix = ".localhost"

var (
	reColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

	// A CSS font-family list: names, quotes, commas, spaces and hyphens only, so
	// it can go into a style rule as is.
	reFont = regexp.MustCompile(`^[A-Za-z0-9 ,'"-]+$`)
)

// Brand is one newsletter site: its own host, sender, look and footer.
type Brand struct {
	Domain           string   `json:"domain"`
	Host             string   `json:"host"`
	ConfigurationSet string   `json:"configurationSet"`
	From             string   `json:"from"`
	ReplyTo          string   `json:"replyTo"`
	Site             string   `json:"site"`
	PostalAddress    []string `json:"postalAddress"` // one entry per line, as on an envelope
	Wordmark         string   `json:"wordmark"`
	Tagline          string   `json:"tagline"`
	Font             string   `json:"font"`
	Logo             *Logo    `json:"logo"`
	Colors           Colors   `json:"colors"`
	Copy             Copy     `json:"copy"`

	// Name is the brand's display name, from the registry's domains[].brand.
	Name string `json:"-"`
}

// Logo is an image served over HTTPS (e-mail clients do not load data: URIs).
type Logo struct {
	URL    string `json:"url"`
	Alt    string `json:"alt"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Colors are the page palette, as #rrggbb.
type Colors struct {
	Page       string `json:"page"`
	Panel      string `json:"panel"`
	Text       string `json:"text"`
	Muted      string `json:"muted"`
	Header     string `json:"header"`
	HeaderText string `json:"headerText"`
	Accent     string `json:"accent"`
	AccentText string `json:"accentText"`
	Link       string `json:"link"`
}

// Copy is the brand's own wording on the subscribe page.
type Copy struct {
	SubscribeTitle string `json:"subscribeTitle"`
	SubscribeIntro string `json:"subscribeIntro"`
}

// RootURL is the brand's public base URL, used for every link listmonk
// generates for it (unsubscribe, opt-in, tracking, archive).
func (b *Brand) RootURL() string {
	return "https://" + b.Host
}

// CSSVars are the brand's palette and font as CSS custom properties
// (--brand-page, --brand-text, ...) for a :root rule. Every value was checked
// when the registry was loaded, which is what makes it safe as template.CSS.
func (b *Brand) CSSVars() template.CSS {
	c := b.Colors
	return template.CSS(fmt.Sprintf("--brand-page:%s;--brand-panel:%s;--brand-text:%s;--brand-muted:%s;"+
		"--brand-header:%s;--brand-header-text:%s;--brand-accent:%s;--brand-accent-text:%s;--brand-link:%s;--brand-font:%s;",
		c.Page, c.Panel, c.Text, c.Muted, c.Header, c.HeaderText, c.Accent, c.AccentText, c.Link, b.Font))
}

// FontCSS is the brand's font-family list for a style attribute (see CSSVars).
func (b *Brand) FontCSS() template.CSS {
	return template.CSS(b.Font)
}

// FromHeader is the From value for mail the brand sends: "Name <address>".
func (b *Brand) FromHeader() string {
	return fmt.Sprintf("%s <%s>", b.Name, b.From)
}

// ListTag is the tag that puts a list under this brand.
func (b *Brand) ListTag() string {
	return ListTagPrefix + b.Domain
}

// HasPostalAddress tells whether the brand may send: CAN-SPAM requires a
// physical postal address in every commercial message.
func (b *Brand) HasPostalAddress() bool {
	for _, line := range b.PostalAddress {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

// Guard holds the staged-send and auto-pause rules for every campaign.
type Guard struct {
	// No rate is judged before this many messages went out.
	MinSent int `json:"minSent"`

	// A campaign pauses itself when hard bounces / sent or complaints / sent
	// pass these fractions.
	MaxBounceRate    float64 `json:"maxBounceRate"`
	MaxComplaintRate float64 `json:"maxComplaintRate"`

	// Cumulative sent counts at which a campaign holds for StageHold, so
	// that bounces and complaints arrive before it sends to more people.
	Stages           []int `json:"stages"`
	StageHoldMinutes int   `json:"stageHoldMinutes"`
}

// StageHold is the wait at each stage.
func (g Guard) StageHold() time.Duration {
	return time.Duration(g.StageHoldMinutes) * time.Minute
}

// Registry is the set of brands listmonk serves.
type Registry struct {
	AdminHost string
	Guard     Guard

	// SourceURL is where the running code is published: the AGPL asks that
	// people using it over the network can get the source, so the public
	// pages link to it.
	SourceURL string

	brands []*Brand
	byHost map[string]*Brand
}

type registryFile struct {
	Domains []struct {
		Name  string `json:"name"`
		Brand string `json:"brand"`
	} `json:"domains"`
	Accounts []struct {
		Address string   `json:"address"`
		Aliases []string `json:"aliases"`
	} `json:"accounts"`
	Marketing *struct {
		AdminHost string   `json:"adminHost"`
		SourceURL string   `json:"sourceUrl"`
		Guard     Guard    `json:"guard"`
		Brands    []*Brand `json:"brands"`
	} `json:"marketing"`
}

// Load reads and validates the mail registry at path.
func Load(path string) (*Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading the brands registry: %v", err)
	}

	return Parse(b)
}

// Parse validates a mail registry document and indexes its brands.
func Parse(b []byte) (*Registry, error) {
	var f registryFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("error parsing the brands registry: %v", err)
	}
	if f.Marketing == nil {
		return nil, fmt.Errorf("the registry has no marketing section")
	}

	names := make(map[string]string, len(f.Domains))
	for _, d := range f.Domains {
		names[d.Name] = d.Brand
	}
	addresses := make(map[string]bool)
	for _, a := range f.Accounts {
		addresses[a.Address] = true
		for _, al := range a.Aliases {
			addresses[al] = true
		}
	}

	r := &Registry{
		AdminHost: strings.ToLower(f.Marketing.AdminHost),
		SourceURL: f.Marketing.SourceURL,
		Guard:     f.Marketing.Guard,
		brands:    f.Marketing.Brands,
		byHost:    make(map[string]*Brand, len(f.Marketing.Brands)),
	}

	var problems []string
	for _, br := range r.brands {
		name, ok := names[br.Domain]
		if !ok {
			problems = append(problems, fmt.Sprintf("brand %q is not a registry domain", br.Domain))
			continue
		}
		br.Name = name
		br.Host = strings.ToLower(br.Host)
		br.From = strings.ToLower(br.From)

		if !strings.HasSuffix(br.Host, "."+br.Domain) {
			problems = append(problems, fmt.Sprintf("%s: host %q is not under the domain", br.Domain, br.Host))
		}
		if _, dup := r.byHost[br.Host]; dup {
			problems = append(problems, fmt.Sprintf("host %q appears twice", br.Host))
		}
		r.byHost[br.Host] = br

		if !utils.ValidateEmail(br.From) || !strings.HasSuffix(br.From, "@"+br.Host) {
			problems = append(problems, fmt.Sprintf("%s: from %q must be an address at %s", br.Domain, br.From, br.Host))
		}
		if !addresses[br.ReplyTo] {
			problems = append(problems, fmt.Sprintf("%s: replyTo %q is not a registry mailbox or alias", br.Domain, br.ReplyTo))
		}
		if br.ConfigurationSet == "" {
			problems = append(problems, fmt.Sprintf("%s: no configurationSet", br.Domain))
		}
		if !reFont.MatchString(br.Font) {
			problems = append(problems, fmt.Sprintf("%s: font %q may only hold names, quotes, commas, spaces and hyphens", br.Domain, br.Font))
		}
		if br.Logo != nil && !strings.HasPrefix(br.Logo.URL, "https://") {
			problems = append(problems, fmt.Sprintf("%s: logo url must be https", br.Domain))
		}
		for field, c := range map[string]string{
			"page": br.Colors.Page, "panel": br.Colors.Panel, "text": br.Colors.Text, "muted": br.Colors.Muted,
			"header": br.Colors.Header, "headerText": br.Colors.HeaderText, "accent": br.Colors.Accent,
			"accentText": br.Colors.AccentText, "link": br.Colors.Link,
		} {
			if !reColor.MatchString(c) {
				problems = append(problems, fmt.Sprintf("%s: colors.%s %q is not #rrggbb", br.Domain, field, c))
			}
		}
	}
	if !strings.HasPrefix(r.SourceURL, "https://") {
		problems = append(problems, "sourceUrl must be the https URL of the published source")
	}
	if _, ok := r.byHost[r.AdminHost]; !ok {
		problems = append(problems, fmt.Sprintf("adminHost %q is not a brand host", r.AdminHost))
	}

	g := r.Guard
	if g.MinSent < 1 || g.MaxBounceRate <= 0 || g.MaxComplaintRate <= 0 || g.StageHoldMinutes < 0 {
		problems = append(problems, "guard needs minSent >= 1, maxBounceRate > 0, maxComplaintRate > 0, stageHoldMinutes >= 0")
	}
	for i, s := range g.Stages {
		if s < 1 || (i > 0 && s <= g.Stages[i-1]) {
			problems = append(problems, "guard stages must be positive and increasing")
			break
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("the brands registry is invalid: %s", strings.Join(problems, "; "))
	}

	return r, nil
}

// All returns every brand in registry order.
func (r *Registry) All() []*Brand {
	return r.brands
}

// ByHost returns the brand whose site is at host (a Host header, port and
// the .localhost development suffix allowed), or nil.
func (r *Registry) ByHost(host string) *Brand {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, devHostSuffix)

	return r.byHost[host]
}

// ByAddress returns the brand that sends from the given From value
// ("Name <a@news.example.com>" or a bare address), or nil.
func (r *Registry) ByAddress(from string) *Brand {
	addr := utils.ParseEmailAddress(from)
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return nil
	}

	return r.byHost[addr[at+1:]]
}

// ByListTags returns the brand a list's tags put it under, or nil when the
// list has no brand tag or more than one.
func (r *Registry) ByListTags(tags []string) *Brand {
	var found *Brand
	for _, t := range tags {
		if !strings.HasPrefix(t, ListTagPrefix) {
			continue
		}
		b := r.byDomain(strings.TrimPrefix(t, ListTagPrefix))
		if b == nil || (found != nil && found != b) {
			return nil
		}
		found = b
	}

	return found
}

func (r *Registry) byDomain(domain string) *Brand {
	for _, b := range r.brands {
		if b.Domain == domain {
			return b
		}
	}

	return nil
}

// PauseReason judges a campaign's rates, using sent as the denominator as SES
// does for its account reviews. It returns why the campaign must pause, or ""
// while the rates are fine or there are fewer than MinSent messages to judge.
func (g Guard) PauseReason(sent, hardBounces, complaints int) string {
	if sent < g.MinSent {
		return ""
	}
	if rate := float64(hardBounces) / float64(sent); rate > g.MaxBounceRate {
		return fmt.Sprintf("hard bounce rate %.2f%% (%d of %d sent) is over %.2f%%",
			rate*100, hardBounces, sent, g.MaxBounceRate*100)
	}
	if rate := float64(complaints) / float64(sent); rate > g.MaxComplaintRate {
		return fmt.Sprintf("complaint rate %.3f%% (%d of %d sent) is over %.3f%%",
			rate*100, complaints, sent, g.MaxComplaintRate*100)
	}

	return ""
}

// NextStage returns the next stage boundary above queued, or 0 when every
// stage has been passed.
func (g Guard) NextStage(queued int) int {
	for _, s := range g.Stages {
		if s > queued {
			return s
		}
	}

	return 0
}
