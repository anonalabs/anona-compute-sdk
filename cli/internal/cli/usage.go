package cli

import (
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

func newUsageCmd(a *app) *cobra.Command {
	var period string
	var billing bool
	var top int
	c := &cobra.Command{
		Use:   "usage",
		Short: "Show usage and cost for a billing period",
		Long: `Show the organization's usage and cost for a billing period (GET /v1/usage on the gateway):
meters, totals with the part not yet rated, a projection for the current month, and the
most expensive sandboxes.

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
			u, err := cl.Usage(a.ctx, period)
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
			if top > 0 && len(u.Sandboxes) > 0 {
				fmt.Fprintf(a.stdout, "\nTop sandboxes (%d of %d):\n", min(top, len(u.Sandboxes)), u.SandboxCount)
				tw = tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "SANDBOX ID\tTEMPLATE\tSTATE\tRUNNING HOURS\tAMOUNT")
				for i, s := range u.Sandboxes {
					if i == top {
						break
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f\t$%.4f\n", s.SandboxID, s.Template, s.State, s.RunningHours, s.AmountUSD)
				}
				_ = tw.Flush()
			}
			return nil
		},
	}
	c.Flags().StringVar(&period, "period", "current", "billing period: current or YYYY-MM")
	c.Flags().BoolVar(&billing, "billing", false, "show plan, rates and invoices instead (owners, admins, keys with billing:read)")
	c.Flags().IntVar(&top, "top", 5, "list this many of the most expensive sandboxes (0 = none)")
	return c
}
