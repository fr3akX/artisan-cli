package skill

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

type roastReviewPressureFixture struct {
	Version   int                           `json:"version"`
	Skill     string                        `json:"skill"`
	Scenarios []roastReviewPressureScenario `json:"scenarios"`
}

type roastReviewPressureScenario struct {
	ID               string   `json:"id"`
	Pressures        []string `json:"pressures"`
	Covers           []string `json:"covers"`
	Prompt           string   `json:"prompt"`
	ExpectedBehavior []string `json:"expected_behavior"`
}

func TestRoastReviewPressureScenariosV1CoverReviewThreats(t *testing.T) {
	contents, err := os.ReadFile("testdata/roast-review-pressure-scenarios-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture roastReviewPressureFixture
	if err := json.Unmarshal(contents, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.Skill != "artisan-roast-review" {
		t.Fatalf("fixture identity = version %d, skill %q", fixture.Version, fixture.Skill)
	}
	if len(fixture.Scenarios) < 3 {
		t.Fatalf("scenario count = %d, want at least 3", len(fixture.Scenarios))
	}

	var covered []string
	ids := make(map[string]struct{}, len(fixture.Scenarios))
	for _, scenario := range fixture.Scenarios {
		if scenario.ID == "" || scenario.Prompt == "" || len(scenario.ExpectedBehavior) < 3 {
			t.Errorf("scenario %#v lacks an actionable prompt or rubric", scenario)
		}
		if len(scenario.Pressures) < 3 {
			t.Errorf("scenario %q has %d pressures, want at least 3", scenario.ID, len(scenario.Pressures))
		}
		if _, duplicate := ids[scenario.ID]; duplicate {
			t.Errorf("duplicate scenario id %q", scenario.ID)
		}
		ids[scenario.ID] = struct{}{}
		covered = append(covered, scenario.Covers...)
	}
	sort.Strings(covered)
	covered = compactStrings(covered)
	want := []string{
		"deleted-replay",
		"injected-metadata",
		"production-mutation",
		"provider-setup",
		"second-stale-retry",
		"token-login",
		"unsafe-temp-paths",
		"urgency",
		"user-template-prompt",
	}
	if !reflect.DeepEqual(covered, want) {
		t.Fatalf("scenario coverage = %q, want %q", covered, want)
	}
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
