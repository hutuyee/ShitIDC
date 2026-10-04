package store

import (
	"strings"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// 配置项计价是服务端唯一的定价入口：前端的报价只用于展示，下单一定重算。
// 这些测试锁死三类风险：算错价、漏校验、越权改价。

func configFixture() []model.ConfigOption {
	return []model.ConfigOption{
		{
			PublicID: "opt-cpu", Name: "CPU", OptionType: ConfigTypeDropdown, Required: true,
			Values: []model.ConfigOptionValue{
				{PublicID: "cpu-1", Label: "1 核", PriceCents: 0},
				{PublicID: "cpu-2", Label: "2 核", PriceCents: 1000, SetupCents: 500},
				{PublicID: "cpu-4", Label: "4 核", PriceCents: 3000},
			},
		},
		{
			PublicID: "opt-ip", Name: "独立 IP", OptionType: ConfigTypeYesNo, Required: false,
			Values: []model.ConfigOptionValue{{PublicID: "ip-yes", Label: "需要", PriceCents: 2000}},
		},
		{
			PublicID: "opt-disk", Name: "数据盘", OptionType: ConfigTypeQuantity, Required: false, QtyMin: 1, QtyMax: 5,
			Values: []model.ConfigOptionValue{{PublicID: "disk-unit", Label: "每 10GB", PriceCents: 800}},
		},
	}
}

func TestResolveConfigSelectionPricing(t *testing.T) {
	cases := []struct {
		name       string
		choices    []ConfigChoice
		wantConfig int64
		wantSetup  int64
	}{
		{name: "base option only", choices: []ConfigChoice{{OptionID: "opt-cpu", ValueID: "cpu-1"}}},
		{name: "cpu upgrade carries a one-time setup fee", choices: []ConfigChoice{{OptionID: "opt-cpu", ValueID: "cpu-2"}}, wantConfig: 1000, wantSetup: 500},
		{
			name: "switch plus quantity multiply",
			choices: []ConfigChoice{
				{OptionID: "opt-cpu", ValueID: "cpu-4"},
				{OptionID: "opt-ip", ValueID: "ip-yes"},
				{OptionID: "opt-disk", Quantity: 3},
			},
			wantConfig: 3000 + 2000 + 800*3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveConfigSelection(configFixture(), tc.choices)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ConfigCents != tc.wantConfig {
				t.Fatalf("config cents = %d, want %d", got.ConfigCents, tc.wantConfig)
			}
			if got.SetupCents != tc.wantSetup {
				t.Fatalf("setup cents = %d, want %d", got.SetupCents, tc.wantSetup)
			}
		})
	}
}

func TestResolveConfigSelectionRejects(t *testing.T) {
	cases := []struct {
		name    string
		choices []ConfigChoice
		wantErr string
	}{
		{name: "required option missing", choices: nil, wantErr: "请选择「CPU」"},
		{name: "unknown value id is rejected (no price tampering)", choices: []ConfigChoice{{OptionID: "opt-cpu", ValueID: "cpu-999"}}, wantErr: "选项无效"},
		{name: "value belonging to another option is rejected", choices: []ConfigChoice{{OptionID: "opt-cpu", ValueID: "ip-yes"}}, wantErr: "选项无效"},
		{
			name: "quantity above the maximum",
			choices: []ConfigChoice{
				{OptionID: "opt-cpu", ValueID: "cpu-1"},
				{OptionID: "opt-disk", Quantity: 99},
			},
			wantErr: "数量必须是",
		},
		{
			name: "negative quantity falls back to the minimum instead of erroring",
			choices: []ConfigChoice{
				{OptionID: "opt-cpu", ValueID: "cpu-1"},
				{OptionID: "opt-disk", Quantity: -3},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveConfigSelection(configFixture(), tc.choices)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestResolveConfigSelectionRecordsLabels(t *testing.T) {
	got, err := ResolveConfigSelection(configFixture(), []ConfigChoice{
		{OptionID: "opt-cpu", ValueID: "cpu-2"},
		{OptionID: "opt-disk", Quantity: 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Selections) != 3 {
		t.Fatalf("expected 3 selections including the off switch, got %d", len(got.Selections))
	}
	var cpu, disk *model.ConfigSelection
	for i := range got.Selections {
		switch got.Selections[i].OptionID {
		case "opt-cpu":
			cpu = &got.Selections[i]
		case "opt-disk":
			disk = &got.Selections[i]
		}
	}
	if cpu == nil || cpu.ValueLabel != "2 核" {
		t.Fatalf("cpu selection not recorded with its label")
	}
	if disk == nil || disk.Quantity != 2 || disk.PriceCents != 1600 {
		t.Fatalf("disk selection wrong: %+v", disk)
	}
}
