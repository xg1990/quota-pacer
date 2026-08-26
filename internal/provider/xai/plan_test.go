package xai

import (
	"testing"

	"quota-pacer/internal/core"
)

func TestClassifyPlan_Paid(t *testing.T) {
	settingsJSON := `{"tier": "supergrok"}`
	class, source := ClassifyPlan([]byte(settingsJSON), nil, "")
	if class != PlanClassPaid {
		t.Errorf("expected PlanClassPaid, got %v (source: %s)", class, source)
	}
}

func TestClassifyPlan_Free(t *testing.T) {
	settingsJSON := `{"tier": "grokfree"}`
	class, source := ClassifyPlan([]byte(settingsJSON), nil, "")
	if class != PlanClassFree {
		t.Errorf("expected PlanClassFree, got %v (source: %s)", class, source)
	}
}

func TestClassifyPlan_UnfetchableDefaultsToFree(t *testing.T) {
	class, source := ClassifyPlan(nil, nil, "")
	if class != PlanClassFree {
		t.Errorf("expected PlanClassFree for unfetchable, got %v (source: %s)", class, source)
	}
}

func TestPlanTypeFromClass(t *testing.T) {
	if pt := planTypeFromClass(PlanClassPaid); pt != core.PlanTypePlus {
		t.Errorf("expected PlanTypePlus for paid, got %v", pt)
	}
	if pt := planTypeFromClass(PlanClassFree); pt != core.PlanTypeFree {
		t.Errorf("expected PlanTypeFree for free, got %v", pt)
	}
}
