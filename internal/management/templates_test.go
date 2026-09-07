package management

import (
	"strings"
	"testing"
)

func TestStatusHTML_HeadroomTableShowsWeightAndResetCredit(t *testing.T) {
	for _, want := range []string{
		`data-i18n="colSchedulingWeight"`,
		`data-i18n="colResetCredit"`,
		`remaining_headroom`,
		`function targetWeight(item)`,
		`function resetCreditInfo(item)`,
		`const priorityDiff=`,
		`(b.target&&b.target.weight)||0`,
	} {
		if !strings.Contains(StatusHTML, want) {
			t.Errorf("status page missing headroom-table contract %q", want)
		}
	}
}
