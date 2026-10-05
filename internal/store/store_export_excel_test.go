package store

import "testing"

// 导出列表的字段选择：去重、去空、拒绝未知列表/字段、空选择报错。
func TestValidateExportColumns(t *testing.T) {
	cols, err := ValidateExportColumns("bill_pay", []string{"bill_num", "amount", "amount", " "})
	if err != nil || len(cols) != 2 || cols[0] != "bill_num" || cols[1] != "amount" {
		t.Fatalf("dedupe -> %v, %v", cols, err)
	}
	if _, err := ValidateExportColumns("bill_pay", []string{"nope"}); err == nil {
		t.Fatal("unknown column must fail")
	}
	if _, err := ValidateExportColumns("nope", []string{"bill_num"}); err == nil {
		t.Fatal("unknown dataset must fail")
	}
	if _, err := ValidateExportColumns("bill_pay", nil); err == nil {
		t.Fatal("empty selection must fail")
	}
}

// 数据集目录：key 与字段 key 都不能重复，且每个列表至少一个字段。
func TestExportDatasetsCatalog(t *testing.T) {
	keys := map[string]bool{}
	for _, d := range ExportDatasets() {
		if d.Key == "" || d.Name == "" || len(d.Columns) == 0 {
			t.Fatalf("dataset incomplete: %+v", d)
		}
		if keys[d.Key] {
			t.Fatalf("duplicate dataset %s", d.Key)
		}
		keys[d.Key] = true
		cols := map[string]bool{}
		for _, c := range d.Columns {
			if cols[c.Key] {
				t.Fatalf("dataset %s duplicate column %s", d.Key, c.Key)
			}
			cols[c.Key] = true
		}
	}
}
