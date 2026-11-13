package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Usage is the part of GET /v1/usage (the metering service's usage report, served by the gateway)
// the CLI shows. Raw keeps the whole document for --json.
type Usage struct {
	Tenant struct {
		ID       string `json:"id"`
		Slug     string `json:"slug"`
		Name     string `json:"name"`
		PlanID   string `json:"planId"`
		PlanName string `json:"planName"`
	} `json:"tenant"`
	Period struct {
		Key          string    `json:"key"`
		Start        time.Time `json:"start"`
		End          time.Time `json:"end"`
		AsOf         time.Time `json:"asOf"`
		RatedThrough time.Time `json:"ratedThrough"`
		Current      bool      `json:"current"`
	} `json:"period"`
	AvailablePeriods []string `json:"availablePeriods"`
	Meters           []struct {
		Meter        string  `json:"meter"`
		Unit         string  `json:"unit"`
		Quantity     float64 `json:"quantity"`
		UnitPriceUSD float64 `json:"unitPriceUsd"`
		AmountUSD    float64 `json:"amountUsd"`
		PendingUSD   float64 `json:"pendingUsd"`
	} `json:"meters"`
	Totals struct {
		RatedUSD     float64  `json:"ratedUsd"`
		PendingUSD   float64  `json:"pendingUsd"`
		AmountUSD    float64  `json:"amountUsd"`
		CreditUSD    float64  `json:"creditUsd"`
		DueUSD       float64  `json:"dueUsd"`
		ProjectedUSD *float64 `json:"projectedUsd"`
	} `json:"totals"`
	Sandboxes    []UsageSandbox `json:"sandboxes"`
	SandboxCount int            `json:"sandboxCount"`
	NextCursor   string         `json:"nextCursor"`

	Raw json.RawMessage `json:"-"`
}

// UsageSandbox is the part of one row of GET /v1/usage's sandboxes the CLI shows as text.
type UsageSandbox struct {
	SandboxID    string  `json:"sandboxId"`
	Template     string  `json:"template"`
	State        string  `json:"state"`
	RunningHours float64 `json:"runningHours"`
	AmountUSD    float64 `json:"amountUsd"`
}

// MaxUsageLimit is the most sandboxes GET /v1/usage returns per page (its limit parameter).
const MaxUsageLimit = 1000

// UsageQuery selects the period and the page of sandboxes of GET /v1/usage.
type UsageQuery struct {
	Period    string // "current" (or "") or YYYY-MM
	Limit     int    // sandboxes per page, 1..MaxUsageLimit; 0 leaves the server's default (50)
	Cursor    string // a previous page's NextCursor
	SandboxID string // only this sandbox's row
}

// Usage calls GET /v1/usage. period is "current" (or "") or YYYY-MM.
func (c *Client) Usage(ctx context.Context, period string) (*Usage, error) {
	return c.UsagePage(ctx, UsageQuery{Period: period})
}

// UsagePage calls GET /v1/usage for one page of sandboxes.
func (c *Client) UsagePage(ctx context.Context, uq UsageQuery) (*Usage, error) {
	q := url.Values{}
	if uq.Period != "" {
		q.Set("period", uq.Period)
	}
	if uq.Limit > 0 {
		q.Set("limit", strconv.Itoa(uq.Limit))
	}
	if uq.Cursor != "" {
		q.Set("cursor", uq.Cursor)
	}
	if uq.SandboxID != "" {
		q.Set("sandboxId", uq.SandboxID)
	}
	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodGet, "/v1/usage", q, nil, &raw, http.StatusOK); err != nil {
		return nil, err
	}
	var u Usage
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, err
	}
	u.Raw = raw
	return &u, nil
}

// Billing is the part of GET /v1/billing (the metering service's billing
// report) the CLI shows as text. Raw keeps the whole document.
type Billing struct {
	Tenant struct {
		Slug         string `json:"slug"`
		Name         string `json:"name"`
		PlanID       string `json:"planId"`
		PlanName     string `json:"planName"`
		BillingModel string `json:"billingModel"`
		Status       string `json:"status"`
	} `json:"tenant"`
	Provider string `json:"provider"`
	Rates    []struct {
		Meter         string    `json:"meter"`
		Tier          string    `json:"tier"`
		Unit          string    `json:"unit"`
		UnitPriceUSD  float64   `json:"unitPriceUsd"`
		EffectiveFrom time.Time `json:"effectiveFrom"`
		MonthlyUSD    *float64  `json:"monthlyUsd"`
	} `json:"rates"`
	Limits struct {
		MonthlyCreditUSD       float64  `json:"monthlyCreditUsd"`
		SpendCapUSD            *float64 `json:"spendCapUsd"`
		SpendCapAction         string   `json:"spendCapAction"`
		MaxConcurrentSandboxes int      `json:"maxConcurrentSandboxes"`
		MaxVCPUPerSandbox      int      `json:"maxVcpuPerSandbox"`
		MaxMemMiBPerSandbox    int      `json:"maxMemMibPerSandbox"`
	} `json:"limits"`
	State *struct {
		PeriodStart time.Time `json:"periodStart"`
		PeriodEnd   time.Time `json:"periodEnd"`
		LedgerUSD   float64   `json:"ledgerUsd"`
		SpendUSD    float64   `json:"spendUsd"`
		State       string    `json:"state"`
	} `json:"state"`
	Invoices []struct {
		DraftID          string    `json:"draftId"`
		PeriodStart      time.Time `json:"periodStart"`
		PeriodEnd        time.Time `json:"periodEnd"`
		Status           string    `json:"status"`
		SubtotalCents    int64     `json:"subtotalCents"`
		CreditCents      int64     `json:"creditCents"`
		TotalCents       int64     `json:"totalCents"`
		CappedTotalCents int64     `json:"cappedTotalCents"`
		UpdatedAt        time.Time `json:"updatedAt"`
	} `json:"invoices"`
	AsOf time.Time `json:"asOf"`

	Raw json.RawMessage `json:"-"`
}

// Billing calls GET /v1/billing (owners, admins and keys with billing:read).
func (c *Client) Billing(ctx context.Context) (*Billing, error) {
	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodGet, "/v1/billing", nil, nil, &raw, http.StatusOK); err != nil {
		return nil, err
	}
	var b Billing
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("decode GET /v1/billing response: %w", err)
	}
	b.Raw = raw
	return &b, nil
}
