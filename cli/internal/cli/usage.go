package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
)

func cents(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s$%d.%02d", sign, c/100, c%100)
}

// printBilling renders GET /v1/billing as text: plan and limits, the
// current period, the rates in effect and the invoices.
func (a *app) printBilling(b *api.Billing) {
	name := first(b.Tenant.Name, b.Tenant.Slug)
	plan := first(b.Tenant.PlanName, b.Tenant.PlanID)
	fmt.Fprintf(a.stdout, "%s, plan %s", name, plan)
	if b.Tenant.BillingModel != "" {
		fmt.Fprintf(a.stdout, " (%s billing)", b.Tenant.BillingModel)
	}
	if b.Provider != "" {
		fmt.Fprintf(a.stdout, ", invoices: %s", b.Provider)
	}
	fmt.Fprintln(a.stdout)
	l := b.Limits
	fmt.Fprintf(a.stdout, "Monthly credit  $%.2f\n", l.MonthlyCreditUSD)
	if l.SpendCapUSD != nil {
		action := ""
		if l.SpendCapAction != "" {
			action = " (then " + l.SpendCapAction + ")"
		}
		fmt.Fprintf(a.stdout, "Spend cap       $%.2f%s\n", *l.SpendCapUSD, action)
	} else {
		fmt.Fprintln(a.stdout, "Spend cap       none")
	}
	if l.MaxConcurrentSandboxes > 0 || l.MaxVCPUPerSandbox > 0 || l.MaxMemMiBPerSandbox > 0 {
		fmt.Fprintf(a.stdout, "Limits          %d sandboxes at once, %d vCPU and %d MiB per sandbox\n",
			l.MaxConcurrentSandboxes, l.MaxVCPUPerSandbox, l.MaxMemMiBPerSandbox)
	}
	if st := b.State; st != nil {
		fmt.Fprintf(a.stdout, "This period     %s to %s: spent $%.4f (%s)\n", st.PeriodStart.UTC().Format(time.DateOnly),
			st.PeriodEnd.UTC().Format(time.DateOnly), st.SpendUSD, st.State)
	}

	fmt.Fprintln(a.stdout, "\nRates:")
	if len(b.Rates) == 0 {
		fmt.Fprintln(a.stdout, "  none")
	} else {
		tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  METER\tTIER\tUNIT\tUNIT PRICE\tPER MONTH\tSINCE")
		for _, r := range b.Rates {
			monthly := "-"
			if r.MonthlyUSD != nil {
				monthly = fmt.Sprintf("$%.4f", *r.MonthlyUSD)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t$%.6f\t%s\t%s\n", r.Meter, first(r.Tier, "-"), r.Unit, r.UnitPriceUSD, monthly,
				r.EffectiveFrom.UTC().Format(time.DateOnly))
		}
		_ = tw.Flush()
	}

	fmt.Fprintln(a.stdout, "\nInvoices:")
	if len(b.Invoices) == 0 {
		fmt.Fprintln(a.stdout, "  none yet")
		return
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  PERIOD\tSTATUS\tSUBTOTAL\tCREDIT\tTOTAL\tUPDATED")
	for _, inv := range b.Invoices {
		total := cents(inv.TotalCents)
		if inv.CappedTotalCents != inv.TotalCents && inv.CappedTotalCents != 0 {
			total += " (capped " + cents(inv.CappedTotalCents) + ")"
		}
		period := inv.PeriodStart.UTC().Format("2006-01")
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", period, strings.ReplaceAll(inv.Status, "_", " "), cents(inv.SubtotalCents),
			cents(inv.CreditCents), total, inv.UpdatedAt.UTC().Format(time.DateOnly))
	}
	_ = tw.Flush()
}

var periodRE = regexp.MustCompile(`^(current|\d{4}-\d{2})$`)

// fetchUsage reads GET /v1/usage with up to want sandboxes, most expensive first, following nextCursor across
// pages of at most api.MaxUsageLimit. want 0 reads one page of the server's default size. The result is the first
// page's document with every fetched row in its sandboxes, and the last page's nextCursor (if rows remain).
func fetchUsage(a *app, cl *api.Client, uq api.UsageQuery, want int) (*api.Usage, error) {
	if want <= 0 {
		return cl.UsagePage(a.ctx, uq)
	}
	var first *api.Usage
	var rows []api.UsageSandbox
	var raws []json.RawMessage
	pages := 0
	for {
		uq.Limit = min(want-len(rows), api.MaxUsageLimit)
		u, err := cl.UsagePage(a.ctx, uq)
		if err != nil {
			return nil, err
		}
		pages++
		var doc struct {
			Sandboxes []json.RawMessage `json:"sandboxes"`
		}
		if err := json.Unmarshal(u.Raw, &doc); err != nil {
			return nil, fmt.Errorf("decode GET /v1/usage response: %w", err)
		}
		if first == nil {
			first = u
			uq.Period = u.Period.Key // "current" must not move to the next month between pages
		}
		rows, raws = append(rows, u.Sandboxes...), append(raws, doc.Sandboxes...)
		first.NextCursor = u.NextCursor
		if len(rows) >= want || u.NextCursor == "" || len(u.Sandboxes) == 0 {
			break
		}
		uq.Cursor = u.NextCursor
	}
	first.Sandboxes = rows
	if pages == 1 {
		return first, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(first.Raw, &doc); err != nil {
		return nil, fmt.Errorf("decode GET /v1/usage response: %w", err)
	}
	doc["sandboxes"], _ = json.Marshal(raws)
	delete(doc, "nextCursor")
	if first.NextCursor != "" {
		doc["nextCursor"], _ = json.Marshal(first.NextCursor)
	}
	merged, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	first.Raw = merged
	return first, nil
}

func newUsageCmd(a *app) *cobra.Command {
	var period, sandbox string
	var billing bool
	var top int
	c := &cobra.Command{
		Use:   "usage",
		Short: "Show usage and cost for a billing period",
		Long: `Show the organization's usage and cost for a billing period (GET /v1/usage on the gateway):
meters, totals with the part not yet rated, a projection for the current month, and the
most expensive sandboxes.

--top N lists the N most expensive sandboxes, reading as many pages of up to 1000 as it
takes; --sandbox ID shows one sandbox's row instead. With --json, --top N puts that many
rows in the document's sandboxes (without --top it holds the gateway's default page of 50),
and next_cursor is set when more remain.

Browser logins of any role may read usage; API keys need the usage:read scope.
--billing prints the plan, rates and invoices instead (GET /v1/billing): owners and admins,
or keys with billing:read. --json prints the gateway's whole document, with snake_case keys.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !periodRE.MatchString(period) {
				return usageErr("--period must be current or YYYY-MM, not %q", period)
			}
			if top < 0 {
				return usageErr("--top must be 0 or more")
			}
			if sandbox != "" && (billing || cmd.Flags().Changed("top")) {
				return usageErr("--sandbox cannot be combined with --top or --billing")
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			if billing {
				b, err := cl.Billing(a.ctx)
				if err != nil {
					return err
				}
				if a.json() {
					a.emit(b.Raw)
					return nil
				}
				a.printBilling(b)
				return nil
			}
			uq := api.UsageQuery{Period: period, SandboxID: sandbox}
			want := top
			if a.json() && !cmd.Flags().Changed("top") {
				want = 0 // the whole default document, as before --top paged
			}
			if sandbox != "" {
				want = 0
			}
			u, err := fetchUsage(a, cl, uq, want)
			if err != nil {
				return err
			}
			if a.json() {
				a.emit(u.Raw)
				return nil
			}
			name := u.Tenant.Name
			if name == "" {
				name = u.Tenant.Slug
			}
			status := "closed"
			if u.Period.Current {
				status = "current, as of " + u.Period.AsOf.UTC().Format("2006-01-02 15:04 UTC")
			}
			fmt.Fprintf(a.stdout, "%s, plan %s, period %s (%s)\n\n", name, u.Tenant.PlanName, u.Period.Key, status)
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
			fmt.Fprintln(tw, "METER\tQUANTITY\tUNIT\tUNIT PRICE\tAMOUNT\t")
			for _, m := range u.Meters {
				fmt.Fprintf(tw, "%s\t%.3f\t%s\t$%.4f\t$%.4f\t\n", m.Meter, m.Quantity, m.Unit, m.UnitPriceUSD, m.AmountUSD)
			}
			_ = tw.Flush()
			t := u.Totals
			fmt.Fprintf(a.stdout, "\nTotal   $%.4f (rated $%.4f + not yet rated $%.4f)\n", t.AmountUSD, t.RatedUSD, t.PendingUSD)
			if t.CreditUSD > 0 {
				fmt.Fprintf(a.stdout, "Credit  $%.4f\n", t.CreditUSD)
			}
			fmt.Fprintf(a.stdout, "Due     $%.4f\n", t.DueUSD)
			if t.ProjectedUSD != nil {
				fmt.Fprintf(a.stdout, "Projected for the month $%.2f\n", *t.ProjectedUSD)
			}
			if in := u.Included; in != nil {
				fmt.Fprintln(a.stdout, "\nAgainst the plan this month:")
				line := func(name, unit string, l api.UsageIncludedLine) {
					if l.Included == nil {
						fmt.Fprintf(a.stdout, "  %-11s %.2f %s, not included ($%.4f)\n", name, l.Used, unit, l.OverageUSD)
						return
					}
					fmt.Fprintf(a.stdout, "  %-11s %.2f of %.0f %s", name, l.Used, *l.Included, unit)
					if l.Over > 0 {
						fmt.Fprintf(a.stdout, ", %.2f over ($%.4f)", l.Over, l.OverageUSD)
					}
					fmt.Fprintln(a.stdout)
				}
				line("vCPU", "vCPU-hours", in.VCPUHours)
				line("Memory", "GiB-hours", in.GiBHours)
				line("Egress", "GB", in.EgressGB)
				line("Storage", "GiB-hours", in.StorageGiBHours)
				switch {
				case in.AtLimit == "stop" && in.Exhausted:
					fmt.Fprintln(a.stdout, "  Used up: new sandboxes are refused and running ones paused until the month ends.")
				case in.AtLimit == "stop":
					fmt.Fprintln(a.stdout, "  When it is used up, new sandboxes stop until the month ends.")
				default:
					fmt.Fprintf(a.stdout, "  Over the included usage: $%.4f, billed on the next invoice.\n", in.OverageUSD)
				}
			}
			rows := u.Sandboxes
			switch {
			case sandbox != "" && len(rows) == 0:
				fmt.Fprintf(a.stdout, "\nSandbox %s has no usage in %s.\n", sandbox, u.Period.Key)
				return nil
			case sandbox != "":
				fmt.Fprintf(a.stdout, "\nSandbox %s:\n", sandbox)
			case top > 0 && len(rows) > 0:
				rows = rows[:min(top, len(rows))]
				fmt.Fprintf(a.stdout, "\nTop sandboxes (%d of %d):\n", len(rows), u.SandboxCount)
			default:
				return nil
			}
			tw = tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SANDBOX ID\tTEMPLATE\tSTATE\tRUNNING HOURS\tAMOUNT")
			for _, s := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f\t$%.4f\n", s.SandboxID, s.Template, s.State, s.RunningHours, s.AmountUSD)
			}
			_ = tw.Flush()
			return nil
		},
	}
	c.Flags().StringVar(&period, "period", "current", "billing period: current or YYYY-MM")
	c.Flags().BoolVar(&billing, "billing", false, "show plan, rates and invoices instead (owners, admins, keys with billing:read)")
	c.Flags().IntVar(&top, "top", 5, "list this many of the most expensive sandboxes, across pages (0 = none)")
	c.Flags().StringVar(&sandbox, "sandbox", "", "show this sandbox's usage row instead of the most expensive ones")
	return c
}
