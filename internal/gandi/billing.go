package gandi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/samouraiworld/yagura/internal/httpx"
	"github.com/samouraiworld/yagura/internal/poller"
)

const billingPath = "/v5/billing/info"

type billingPoller struct {
	client    *httpx.Client
	sharingID string
	interval  time.Duration
	logger    *slog.Logger
}

// NewBillingPoller returns the poller of the prepaid account, named
// "gandi_billing". A non-empty sharingID queries that organization instead of
// the caller's own account.
func NewBillingPoller(client *httpx.Client, sharingID string, interval time.Duration, logger *slog.Logger) poller.Poller {
	return &billingPoller{client: client, sharingID: sharingID, interval: interval, logger: logger}
}

func (p *billingPoller) Name() string            { return "gandi_billing" }
func (p *billingPoller) Interval() time.Duration { return p.interval }

type billingInfo struct {
	Prepaid *struct {
		Amount   *float64 `json:"amount"`
		Currency string   `json:"currency"`
		// WarningThreshold stays raw: an absent or null threshold is a valid
		// account setting, while a present non-number is an item error that
		// must not fail the whole poll. A non-number amount still fails the
		// poll, so the last good balance stays exported.
		WarningThreshold json.RawMessage `json:"warning_threshold"`
	} `json:"prepaid"`
}

// Poll reads the billing info and exports the prepaid balance and warning
// threshold. An account without prepaid yields no series and no error; a
// prepaid account without a threshold yields the balance only, also without
// error.
func (p *billingPoller) Poll(ctx context.Context) (poller.Result, error) {
	path := billingPath
	if p.sharingID != "" {
		path += "/" + p.sharingID
	}
	var info *billingInfo
	if _, err := p.client.GetJSON(ctx, path, nil, &info); err != nil {
		return poller.Result{}, fmt.Errorf("gandi billing: %w", err)
	}
	if info == nil {
		return poller.Result{}, fmt.Errorf("gandi billing: response is not an object")
	}
	pp := info.Prepaid
	if pp == nil {
		return poller.Result{}, nil
	}
	if pp.Currency == "" {
		p.logger.Warn("gandi prepaid has no currency; series omitted")
		return poller.Result{ItemErrors: 1}, nil
	}

	var metrics []prometheus.Metric
	var itemErrors int
	if pp.Amount == nil {
		p.logger.Warn("gandi prepaid field missing; series omitted",
			"field", "amount", "currency", capped(pp.Currency))
		itemErrors++
	} else {
		metrics = append(metrics, gauge(prepaidBalanceDesc, *pp.Amount, pp.Currency))
	}
	if threshold, ok, err := optionalNumber(pp.WarningThreshold); err != nil {
		p.logger.Warn("gandi prepaid field is not a number; series omitted",
			"field", "warning_threshold", "currency", capped(pp.Currency))
		itemErrors++
	} else if ok {
		metrics = append(metrics, gauge(prepaidThresholdDesc, threshold, pp.Currency))
	}
	return poller.Result{Metrics: metrics, ItemErrors: itemErrors}, nil
}

// optionalNumber decodes an optional JSON number. An absent or null value
// gives ok=false without error; any other non-number is an error.
func optionalNumber(raw json.RawMessage) (v float64, ok bool, err error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false, err
	}
	return v, true, nil
}
