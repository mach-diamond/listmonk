package manager

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/knadh/listmonk/models"
	"github.com/paulbellamy/ratecounter"
)

type pipe struct {
	camp       *models.Campaign
	rate       *ratecounter.RateCounter
	wg         *sync.WaitGroup
	sent       atomic.Int64
	lastID     atomic.Uint64
	errors     atomic.Uint64
	stopped    atomic.Bool
	withErrors atomic.Bool

	// Staged sends: messages queued so far (counting those sent before this
	// pipe started), the hold at the last stage boundary, and why the guard
	// paused the campaign. Only the manager's Run loop touches these, and the
	// reason is read by cleanup() after the waitgroup releases.
	queued      int
	nextStage   int
	holdUntil   time.Time
	pauseReason string

	m *Manager
}

// CampaignBounces are the counts the guard judges a running campaign by.
type CampaignBounces struct {
	Sent        int `db:"sent"`
	HardBounces int `db:"hard_bounces"`
	Complaints  int `db:"complaints"`
}

// guardPoll is how long a held campaign yields before the Run loop asks again.
const guardPoll = time.Second

// newPipe adds a campaign to the process queue.
func (m *Manager) newPipe(c *models.Campaign) (*pipe, error) {
	// Validate messenger.
	if _, ok := m.messengers[c.Messenger]; !ok {
		m.store.UpdateCampaignStatus(c.ID, models.CampaignStatusCancelled)
		return nil, fmt.Errorf("unknown messenger %s on campaign %s", c.Messenger, c.Name)
	}

	// A campaign without a brand has no news host to link to and no SES
	// configuration set; it never sends (the API refuses to start one, this
	// catches one that changed after it was scheduled).
	if m.Brand(c) == nil {
		m.store.UpdateCampaignStatus(c.ID, models.CampaignStatusPaused)
		return nil, fmt.Errorf("campaign %s: the From address %q is not a brand's sender", c.Name, c.FromEmail)
	}

	// Resolve any inline images before compiling the template.
	if err := m.LoadInlineImages(c); err != nil {
		return nil, err
	}

	// Load the template.
	if err := c.CompileTemplate(m.TemplateFuncs(c)); err != nil {
		return nil, err
	}

	// Load any media/attachments.
	if err := m.attachMedia(c); err != nil {
		return nil, err
	}

	// Add the campaign to the active map.
	p := &pipe{
		camp:   c,
		rate:   ratecounter.NewRateCounter(time.Minute),
		wg:     &sync.WaitGroup{},
		queued: c.Sent,
		m:      m,
	}
	p.nextStage = m.cfg.Brands.Guard.NextStage(p.queued)

	// Increment the waitgroup so that Wait() blocks immediately. This is necessary
	// as a campaign pipe is created first and subscribers/messages under it are
	// fetched asynchronolusly later. The messages each add to the wg and that
	// count is used to determine the exhaustion/completion of all messages.
	p.wg.Add(1)

	go func() {
		// Wait for all the messages in the campaign to be processed
		// (successfully or skipped after errors or cancellation).
		p.wg.Wait()

		p.cleanup()
	}()

	m.pipesMut.Lock()
	m.pipes[c.ID] = p
	m.pipesMut.Unlock()
	return p, nil
}

// NextSubscribers processes the next batch of subscribers in a given campaign.
// It returns a bool indicating whether any subscribers were processed
// in the current batch or not. A false indicates that all subscribers
// have been processed, or that a campaign has been paused or cancelled.
func (p *pipe) NextSubscribers() (bool, error) {
	// Staged sends: hold at a stage boundary, pause on bad rates.
	limit, err := p.guard()
	if err != nil {
		return false, err
	}
	if limit == 0 {
		if p.stopped.Load() {
			return false, nil
		}
		time.Sleep(guardPoll)
		return true, nil
	}

	// Fetch the next batch of subscribers from a 'running' campaign.
	subs, err := p.m.store.NextSubscribers(p.camp.ID, limit)
	if err != nil {
		return false, fmt.Errorf("error fetching campaign subscribers (%s): %v", p.camp.Name, err)
	}

	// There are no subscribers from the query. Either all subscribers on the campaign
	// have been processed, or the campaign has changed from 'running' to 'paused' or 'cancelled'.
	if len(subs) == 0 {
		return false, nil
	}

	// Is there a sliding window limit configured?
	hasSliding := p.m.cfg.SlidingWindow &&
		p.m.cfg.SlidingWindowRate > 0 &&
		p.m.cfg.SlidingWindowDuration.Seconds() > 1

	// Push messages.
	for _, s := range subs {
		msg, err := p.newMessage(s)
		if err != nil {
			p.m.log.Printf("error rendering message (%s) (%s): %v", p.camp.Name, s.Email, err)
			continue
		}

		// Push the message to the queue while blocking and waiting until
		// the queue is drained.
		p.m.campMsgQ <- msg
		p.queued++

		// Check if the sliding window is active.
		if hasSliding {
			diff := time.Since(p.m.slidingStart)

			// Window has expired. Reset the clock.
			if diff >= p.m.cfg.SlidingWindowDuration {
				p.m.slidingStart = time.Now()
				p.m.slidingCount = 0
			}

			// Have the messages exceeded the limit?
			p.m.slidingCount++
			if p.m.slidingCount >= p.m.cfg.SlidingWindowRate {
				wait := p.m.cfg.SlidingWindowDuration - diff

				p.m.log.Printf("messages exceeded (%d) for the window (%v since %s). Sleeping for %s.",
					p.m.slidingCount,
					p.m.cfg.SlidingWindowDuration,
					p.m.slidingStart.Format(time.RFC822Z),
					wait.Round(time.Second)*1)

				p.m.slidingCount = 0
				time.Sleep(wait)
			}
		}
	}

	return true, nil
}

// guard applies the registry's staged-send rules before a batch. It returns how
// many subscribers the batch may take: 0 while the campaign holds at a stage
// or after the guard paused it (then the pipe is stopped).
//
// The fetch is capped at the next stage boundary, because fetching advances the
// campaign's checkpoint: a subscriber fetched and not queued would be skipped.
// At the boundary the campaign holds for the stage hold so bounces and
// complaints can arrive. Its rates are judged before every batch once enough
// mail went out (and so also at the end of every hold).
func (p *pipe) guard() (int, error) {
	g := p.m.cfg.Brands.Guard

	if time.Now().Before(p.holdUntil) {
		return 0, nil
	}

	if p.queued >= g.MinSent {
		counts, err := p.m.store.GetCampaignBounceCounts(p.camp.ID)
		if err != nil {
			return 0, fmt.Errorf("error fetching bounce counts (%s): %v", p.camp.Name, err)
		}

		// Sent counts are flushed to the DB periodically; add what this pipe has not flushed yet.
		sent := counts.Sent + int(p.sent.Load())
		if reason := g.PauseReason(sent, counts.HardBounces, counts.Complaints); reason != "" {
			p.pauseReason = reason
			p.Stop(true)
			p.m.log.Printf("pausing campaign %s: %s", p.camp.Name, reason)
			return 0, nil
		}
	}

	// A stage boundary reached: hold there before sending to more people.
	if p.nextStage > 0 && p.queued >= p.nextStage {
		p.holdUntil = time.Now().Add(g.StageHold())
		p.m.log.Printf("campaign %s reached stage %d: holding until %s", p.camp.Name, p.nextStage, p.holdUntil.Format(time.RFC822Z))
		p.nextStage = g.NextStage(p.queued)
		return 0, nil
	}

	limit := p.m.cfg.BatchSize
	if p.nextStage > 0 {
		limit = min(limit, p.nextStage-p.queued)
	}

	return limit, nil
}

// OnError keeps track of the number of errors that occur while sending messages
// and pauses the campaign if the error threshold is met.
func (p *pipe) OnError() {
	if p.m.cfg.MaxSendErrors < 1 {
		return
	}

	// If the error threshold is met, pause the campaign.
	count := p.errors.Add(1)
	if int(count) < p.m.cfg.MaxSendErrors {
		return
	}

	p.Stop(true)
	p.m.log.Printf("error count exceeded %d. pausing campaign %s", p.m.cfg.MaxSendErrors, p.camp.Name)
}

// Stop "marks" a campaign as stopped. It doesn't actually stop the processing
// of messages. That happens when every queued message in the campaign is processed,
// marking .wg, the waitgroup counter as done. That triggers cleanup().
func (p *pipe) Stop(withErrors bool) {
	// Already stopped.
	if p.stopped.Load() {
		return
	}

	if withErrors {
		p.withErrors.Store(true)
	}

	p.stopped.Store(true)
}

// newMessage returns a campaign message while internally incrementing the
// number of messages in the pipe wait group so that the status of every
// message can be atomically tracked.
func (p *pipe) newMessage(s models.Subscriber) (CampaignMessage, error) {
	msg, err := p.m.NewCampaignMessage(p.camp, s)
	if err != nil {
		return msg, err
	}

	msg.pipe = p
	p.wg.Add(1)

	return msg, nil
}

// cleanup finishes the campaign and updates the campaign status in the DB
// and also triggers a notification to the admin. This only triggers once
// a pipe's wg counter is fully exhausted, draining all messages in its queue.
func (p *pipe) cleanup() {
	defer func() {
		p.m.pipesMut.Lock()
		delete(p.m.pipes, p.camp.ID)
		p.m.pipesMut.Unlock()
	}()

	// Update campaign's 'sent count.
	if err := p.m.store.UpdateCampaignCounts(p.camp.ID, 0, int(p.sent.Load()), int(p.lastID.Load())); err != nil {
		p.m.log.Printf("error updating campaign counts (%s): %v", p.camp.Name, err)
	}

	// The campaign was auto-paused due to errors.
	if p.withErrors.Load() {
		if err := p.m.store.UpdateCampaignStatus(p.camp.ID, models.CampaignStatusPaused); err != nil {
			p.m.log.Printf("error updating campaign (%s) status to %s: %v", p.camp.Name, models.CampaignStatusPaused, err)
		} else {
			p.m.log.Printf("set campaign (%s) to %s", p.camp.Name, models.CampaignStatusPaused)
		}

		reason := "Too many errors"
		if p.pauseReason != "" {
			reason = p.pauseReason
		}
		_ = p.m.sendNotif(p.camp, models.CampaignStatusPaused, reason)
		return
	}

	// The campaign was manually stopped (pause, cancel).
	if p.stopped.Load() {
		p.m.log.Printf("stop processing campaign (%s)", p.camp.Name)
		return
	}

	// Campaign wasn't manually stopped and subscribers were naturally exhausted.
	// Fetch the up-to-date campaign status from the DB.
	c, err := p.m.store.GetCampaign(p.camp.ID)
	if err != nil {
		p.m.log.Printf("error fetching campaign (%s) for ending: %v", p.camp.Name, err)
		return
	}

	// If a running campaign has exhausted subscribers, it's finished.
	if c.Status == models.CampaignStatusRunning || c.Status == models.CampaignStatusScheduled {
		c.Status = models.CampaignStatusFinished
		if err := p.m.store.UpdateCampaignStatus(p.camp.ID, models.CampaignStatusFinished); err != nil {
			p.m.log.Printf("error finishing campaign (%s): %v", p.camp.Name, err)
		} else {
			p.m.log.Printf("campaign (%s) finished", p.camp.Name)
		}
	} else {
		p.m.log.Printf("finish processing campaign (%s)", p.camp.Name)
	}

	// Notify admin.
	_ = p.m.sendNotif(c, c.Status, "")
}
