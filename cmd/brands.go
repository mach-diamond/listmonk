package main

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/knadh/listmonk/internal/brands"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v4"
)

// requestBrand returns the brand whose news host the request came to, or nil.
// Public pages only ever show and change a brand's own lists and campaigns, so
// a subscriber on two brands' lists manages each one on its own site.
func (a *App) requestBrand(c echo.Context) *brands.Brand {
	return a.brands.ByHost(c.Request().Host)
}

// ofBrand keeps the items whose list tags put them under brand b; nil keeps none.
func ofBrand[T any](r *brands.Registry, b *brands.Brand, items []T, tags func(T) []string) []T {
	out := make([]T, 0, len(items))
	if b == nil {
		return out
	}
	for _, it := range items {
		if r.ByListTags(tags(it)) == b {
			out = append(out, it)
		}
	}

	return out
}

// brandLists keeps the lists that belong to brand b.
func (a *App) brandLists(b *brands.Brand, lists []models.List) []models.List {
	return ofBrand(a.brands, b, lists, func(l models.List) []string { return l.Tags })
}

// brandSubscriptions keeps the subscriptions to lists that belong to brand b.
func (a *App) brandSubscriptions(b *brands.Brand, subs []models.Subscription) []models.Subscription {
	return ofBrand(a.brands, b, subs, func(s models.Subscription) []string { return s.Tags })
}

// maxMessageBytes is where Gmail clips a message and hides what follows,
// including the unsubscribe link.
const maxMessageBytes = 100_000

// checkCampaignSendable refuses to start or schedule a campaign that is not
// cleanly one brand's: its From address must be a brand's sender, the brand
// must have a postal address (CAN-SPAM), every list must carry that brand's
// tag, and the rendered message must link to the brand's unsubscribe page and
// stay under Gmail's clipping size.
func (a *App) checkCampaignSendable(camp models.Campaign) error {
	brand := a.manager.Brand(&camp)
	if brand == nil {
		senders := make([]string, 0, len(a.brands.All()))
		for _, b := range a.brands.All() {
			senders = append(senders, b.FromHeader())
		}
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("the From address must be a brand's sender, one of: %s", strings.Join(senders, ", ")))
	}

	if !brand.HasPostalAddress() {
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("%s has no postal address in the mail registry; every newsletter needs one", brand.Name))
	}

	var campLists []struct {
		ID int `json:"id"`
	}
	if err := camp.Lists.Unmarshal(&campLists); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("error reading the campaign's lists: %v", err))
	}
	ids := make([]int, 0, len(campLists))
	for _, l := range campLists {
		ids = append(ids, l.ID)
	}
	lists, err := a.core.GetLists("", "", false, ids)
	if err != nil {
		return err
	}
	for _, l := range lists {
		if a.brands.ByListTags(l.Tags) != brand {
			return echo.NewHTTPError(http.StatusBadRequest,
				fmt.Sprintf("list %q is not tagged %s; a campaign sends to one brand's lists only", l.Name, brand.ListTag()))
		}
	}

	if err := camp.CompileTemplate(a.manager.TemplateFuncs(&camp)); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, a.i18n.Ts("templates.errorCompiling", "error", err.Error()))
	}
	msg, err := a.manager.NewCampaignMessage(&camp, dummySubscriber)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, a.i18n.Ts("templates.errorRendering", "error", err.Error()))
	}
	body := msg.Body()
	if !bytes.Contains(body, []byte(brand.RootURL()+"/subscription/")) {
		return echo.NewHTTPError(http.StatusBadRequest,
			"the message has no unsubscribe link: add {{ UnsubscribeURL }} to the template or the body")
	}
	if len(body) > maxMessageBytes {
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("the message is %d KB; Gmail clips messages over %d KB", len(body)/1000, maxMessageBytes/1000))
	}

	return nil
}
