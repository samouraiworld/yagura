package scaleway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
)

// historyTick is how often the history poller checks which reads are due.
// A check with nothing due makes no API call.
const historyTick = 15 * time.Minute

type historyPoller struct {
	client         *httpx.Client
	organizationID string
	history        *History
	logger         *slog.Logger
}

// NewHistoryPoller returns the poller of the closed months' consumption and
// of the invoices, named "scaleway_history". The current month comes from
// the consumption poller. The poller exposes no metric itself: the history
// collector does, at scrape time.
func NewHistoryPoller(client *httpx.Client, organizationID string, history *History, logger *slog.Logger) poller.Poller {
	return &historyPoller{client: client, organizationID: organizationID, history: history, logger: logger}
}

func (p *historyPoller) Name() string            { return "scaleway_history" }
func (p *historyPoller) Interval() time.Duration { return historyTick }

// Poll reads the due closed months, newest first, then the invoice list if
// it is due, and saves the history. A failed read keeps the stored values,
// logs one error line and is retried an hour later; the poll then fails, so
// that yagura_collector_up shows it, until every read has succeeded again.
func (p *historyPoller) Poll(ctx context.Context) (poller.Result, error) {
	h := p.history
	now := h.now()
	h.prune(now)
	months, invoices := h.due(now)
	itemErrors := 0
	// outOfTime is set when the poll deadline (or shutdown) stops a read:
	// that is not a failure of the month, which stays due for the next check.
	outOfTime, authFailed := false, false
	stopped := func(err error) bool {
		return ctx.Err() != nil || errors.Is(err, httpx.ErrPollDeadline)
	}
	for _, period := range months {
		read, err := readConsumption(ctx, p.client, p.organizationID, period, p.logger)
		if err != nil && stopped(err) {
			outOfTime = true
			break
		}
		if err != nil {
			p.logger.Error("scaleway history: cannot read a month; stored values kept, retried in 1h",
				"billing_period", period, "error", err)
			h.fail(period, h.now())
			if httpx.IsAuthError(err) {
				// A refused key fails every read: stop here rather than log
				// one error per month; the other reads stay due.
				authFailed = true
				break
			}
			continue
		}
		itemErrors += read.itemErrors
		h.setMonth(period, monthLines(read.totals), h.now())
	}
	if invoices && !outOfTime && !authFailed {
		cur := period(now)
		amounts, err := readInvoices(ctx, p.client, p.organizationID, func(pr string) bool {
			return pr != cur && h.inWindow(pr, now)
		})
		switch {
		case err != nil && stopped(err):
			outOfTime = true
		case err != nil:
			p.logger.Error("scaleway history: cannot read the invoices; stored values kept, retried in 1h", "error", err)
			h.fail(invoicesKey, h.now())
		default:
			h.setInvoices(amounts, h.now())
		}
	}
	if err := h.save(); err != nil {
		return poller.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return poller.Result{}, err
	}
	if outOfTime {
		return poller.Result{}, errors.New("scaleway history: poll deadline reached; the remaining reads continue at the next check")
	}
	if failing := h.failing(); len(failing) > 0 {
		return poller.Result{}, fmt.Errorf("scaleway history: last read failed for %s; retried hourly", strings.Join(failing, ", "))
	}
	return poller.Result{ItemErrors: itemErrors}, nil
}
