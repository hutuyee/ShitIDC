package store

import (
	"strings"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// 条件联动的两条安全性质：隐藏项不计费、联动必填必须选。

// linkFixture：选「机房」= 香港 时，才出现并必填「带宽」；选 美国 时不出现。
func linkFixture() ([]model.ConfigOption, []ConfigLink) {
	options := []model.ConfigOption{
		{
			PublicID: "opt-region", Name: "机房", OptionType: ConfigTypeDropdown, Required: true,
			Values: []model.ConfigOptionValue{
				{PublicID: "r-hk", Label: "香港", PriceCents: 0},
				{PublicID: "r-us", Label: "美国", PriceCents: 0},
			},
		},
		{
			PublicID: "opt-bw", Name: "带宽", OptionType: ConfigTypeDropdown, Required: false,
			Values: []model.ConfigOptionValue{
				{PublicID: "bw-10", Label: "10M", PriceCents: 500},
				{PublicID: "bw-100", Label: "100M", PriceCents: 5000},
			},
		},
	}
	links := []ConfigLink{
		{SourceOptionID: "opt-region", SourceValueID: "r-hk", TargetOptionID: "opt-bw", Visible: true, Required: true},
		{SourceOptionID: "opt-region", SourceValueID: "r-us", TargetOptionID: "opt-bw", Visible: false, Required: false},
	}
	return options, links
}

func TestLinkageHidesOptionAndItsPrice(t *testing.T) {
	options, links := linkFixture()
	// 选了美国（带宽应隐藏），前端仍塞了一个带宽值试图加价 —— 必须被忽略。
	got, err := ResolveConfigSelectionWithLinks(options, links, []ConfigChoice{
		{OptionID: "opt-region", ValueID: "r-us"},
		{OptionID: "opt-bw", ValueID: "bw-100"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ConfigCents != 0 {
		t.Fatalf("hidden option was charged: config cents = %d, want 0", got.ConfigCents)
	}
	for _, sel := range got.Selections {
		if sel.OptionID == "opt-bw" {
			t.Fatal("hidden option was recorded in the order selections")
		}
	}
}

func TestLinkageRequiresDependentOption(t *testing.T) {
	options, links := linkFixture()
	// 选了香港 → 带宽被联动为必填，不选就必须报错。
	_, err := ResolveConfigSelectionWithLinks(options, links, []ConfigChoice{
		{OptionID: "opt-region", ValueID: "r-hk"},
	})
	if err == nil {
		t.Fatal("expected the linked-required option to be enforced")
	}
	if !strings.Contains(err.Error(), "带宽") {
		t.Fatalf("error %q should name the missing option", err.Error())
	}
}

func TestLinkageChargesVisibleOption(t *testing.T) {
	options, links := linkFixture()
	got, err := ResolveConfigSelectionWithLinks(options, links, []ConfigChoice{
		{OptionID: "opt-region", ValueID: "r-hk"},
		{OptionID: "opt-bw", ValueID: "bw-100"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ConfigCents != 5000 {
		t.Fatalf("config cents = %d, want 5000", got.ConfigCents)
	}
}

func TestNoLinksKeepsOriginalSemantics(t *testing.T) {
	options, _ := linkFixture()
	// 没有联动规则时，带宽保持“非必填、可见”，行为与从前一致。
	got, err := ResolveConfigSelectionWithLinks(options, nil, []ConfigChoice{
		{OptionID: "opt-region", ValueID: "r-us"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ConfigCents != 0 {
		t.Fatalf("config cents = %d, want 0", got.ConfigCents)
	}
	// 显式选了带宽就要计费。
	got2, err := ResolveConfigSelectionWithLinks(options, nil, []ConfigChoice{
		{OptionID: "opt-region", ValueID: "r-us"},
		{OptionID: "opt-bw", ValueID: "bw-10"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got2.ConfigCents != 500 {
		t.Fatalf("config cents = %d, want 500", got2.ConfigCents)
	}
}
