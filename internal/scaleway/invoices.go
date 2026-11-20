package scaleway

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/samouraiworld/yagura/internal/httpx"
)

const invoicesPath = "/billing/v2beta1/invoices"

// Invoice amount kinds, the values of the kind label.
const (
	KindUntaxed  = "untaxed"
	KindDiscount = "discount"
)

type invoice struct {
	ID            string `json:"id"`
	BillingPeriod string `json:"billing_period"`
	TotalUntaxed  *money `json:"total_untaxed"`
	TotalDiscount *money `json:"total_discount"`
}

type invoicesPage struct {
	Invoices   []invoice `json:"invoices"` // nil when absent or null
	TotalCount *int      `json:"total_count"`
}

// InvoiceAmount is the sum of one kind of invoice total over the invoices of
// a billing period, in one currency.
type InvoiceAmount struct {
	BillingPeriod string  `json:"billing_period"`
	Kind          string  `json:"kind"`
	Currency      string  `json:"currency"`
	Amount        float64 `json:"amount"`
}

type invoiceKey struct {
	period, kind, currency string
}

// invoicePeriod returns the "YYYY-MM" billing period of an invoice. The API
// sends an RFC 3339 time at midnight UTC on the first day of the month;
// billing periods are calendar months in UTC.
func invoicePeriod(s string) (string, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(periodLayout), nil
	}
	if t, err := time.Parse(periodLayout, s); err == nil {
		return t.Format(periodLayout), nil
	}
	return "", fmt.Errorf("billing_period %q is neither RFC 3339 nor YYYY-MM", capped(s))
}

// readInvoices lists every invoice of the organization, oldest number first
// so that a new invoice does not shift the pages, and sums total_untaxed and
// total_discount per billing period and currency, with the API's sign. An
// invoice seen twice (same ID) counts once. An invoice without an ID, a
// usable billing period or total_untaxed fails the read, and so does an empty
// page before total_count: a sum without them would be wrong. A missing
// total_discount counts as 0.
//
// keep, when not nil, selects the billing periods to sum; an invoice of
// another period is not checked further, so that an old malformed invoice
// does not block the recent totals.
func readInvoices(ctx context.Context, client *httpx.Client, organizationID string, keep func(period string) bool) ([]InvoiceAmount, error) {
	sums := map[invoiceKey]total{}
	seen := map[string]bool{}
	for page := 1; page <= httpx.MaxPages; page++ {
		query := url.Values{
			"organization_id": {organizationID},
			"order_by":        {"invoice_number_asc"},
			"page":            {strconv.Itoa(page)},
			"page_size":       {strconv.Itoa(pageSize)},
		}
		var resp invoicesPage
		if _, err := client.GetJSON(ctx, invoicesPath, query, &resp); err != nil {
			return nil, fmt.Errorf("scaleway invoices: page %d: %w", page, err)
		}
		if resp.Invoices == nil {
			return nil, fmt.Errorf("scaleway invoices: page %d: missing \"invoices\" array", page)
		}
		for _, inv := range resp.Invoices {
			// The ID de-duplicates an invoice that a shifted page returns twice.
			if inv.ID == "" {
				return nil, fmt.Errorf("scaleway invoices: page %d: an invoice has no id", page)
			}
			if seen[inv.ID] {
				continue
			}
			seen[inv.ID] = true
			if err := addInvoice(sums, inv, keep); err != nil {
				return nil, fmt.Errorf("scaleway invoices: invoice %s: %w", capped(inv.ID), err)
			}
		}
		// Distinct invoices are counted: a duplicate from a shifted page
		// must not end the listing early.
		if len(resp.Invoices) == 0 && resp.TotalCount != nil && len(seen) < *resp.TotalCount {
			return nil, fmt.Errorf("scaleway invoices: page %d is empty after %d of %d invoices", page, len(seen), *resp.TotalCount)
		}
		if len(resp.Invoices) == 0 || (resp.TotalCount != nil && len(seen) >= *resp.TotalCount) {
			return invoiceAmounts(sums), nil
		}
	}
	return nil, fmt.Errorf("scaleway invoices: still incomplete after %d pages", httpx.MaxPages)
}

func addInvoice(sums map[invoiceKey]total, inv invoice, keep func(string) bool) error {
	period, err := invoicePeriod(inv.BillingPeriod)
	if err != nil {
		return err
	}
	if keep != nil && !keep(period) {
		return nil
	}
	untaxed := inv.TotalUntaxed
	if untaxed == nil || untaxed.CurrencyCode == "" {
		return fmt.Errorf("no total_untaxed or no currency")
	}
	discount := inv.TotalDiscount
	if discount == nil || (discount.CurrencyCode == "" && discount.Units == 0 && discount.Nanos == 0) {
		discount = &money{CurrencyCode: untaxed.CurrencyCode}
	}
	if discount.CurrencyCode == "" {
		return fmt.Errorf("total_discount has no currency")
	}
	for _, v := range []struct {
		kind string
		m    *money
	}{{KindUntaxed, untaxed}, {KindDiscount, discount}} {
		k := invoiceKey{period, v.kind, v.m.CurrencyCode}
		t := sums[k]
		t.units += v.m.Units
		t.nanos += int64(v.m.Nanos)
		sums[k] = t
	}
	return nil
}

func invoiceAmounts(sums map[invoiceKey]total) []InvoiceAmount {
	out := make([]InvoiceAmount, 0, len(sums))
	for k, t := range sums {
		out = append(out, InvoiceAmount{BillingPeriod: k.period, Kind: k.kind, Currency: k.currency, Amount: t.amount()})
	}
	sortInvoiceAmounts(out)
	return out
}

func sortInvoiceAmounts(a []InvoiceAmount) {
	slices.SortFunc(a, func(x, y InvoiceAmount) int {
		return cmp.Or(cmp.Compare(x.BillingPeriod, y.BillingPeriod), cmp.Compare(x.Kind, y.Kind), cmp.Compare(x.Currency, y.Currency))
	})
}
