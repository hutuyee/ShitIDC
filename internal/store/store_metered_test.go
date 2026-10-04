package store

import "testing"

// 超量计费直接决定向客户多收多少钱，逐条钉死。
// 关键性质：水位机制必须保证「同一段用量只收一次钱」。

func meteredPlan() MeteredPlan {
	return MeteredPlan{
		Enabled:           true,
		DiskIncludedMB:    20 * 1024, // 20GB 包含
		DiskLimitMB:       50 * 1024, // 50GB 硬上限
		DiskPricePerGB:    100,       // 1 元/GB
		BWIncludedGB:      500,
		BWLimitGB:         1000,
		BWPricePerGBCents: 20, // 0.2 元/GB
		Billing:           "cycle_end",
	}
}

func TestComputeOverageWithinQuota(t *testing.T) {
	q := ComputeOverage(meteredPlan(), 10*1024, 0, 300, 0)
	if len(q.Lines) != 0 || q.TotalCents != 0 {
		t.Fatalf("usage inside the included quota must be free, got %+v", q)
	}
	if q.OverHardLimit {
		t.Fatal("usage inside the quota cannot be over the hard limit")
	}
}

func TestComputeOverageDiskRoundsUpToWholeGB(t *testing.T) {
	// 21GB 用量、20GB 包含 → 超 1GB，收 1 元。
	q := ComputeOverage(meteredPlan(), 21*1024, 0, 0, 0)
	if q.TotalCents != 100 {
		t.Fatalf("total = %d, want 100", q.TotalCents)
	}
	// 超出 1MB 也要向上取整成 1GB：不能让超量变成 0 元。
	q2 := ComputeOverage(meteredPlan(), 20*1024+1, 0, 0, 0)
	if q2.TotalCents != 100 {
		t.Fatalf("1MB over must round up to 1GB and cost 100, got %d", q2.TotalCents)
	}
	// 超出 1GB+1 就要按 2GB 计。
	q3 := ComputeOverage(meteredPlan(), 22*1024+1, 0, 0, 0)
	if q3.TotalCents != 300 {
		t.Fatalf("2GB+1MB over must bill 3GB = 300, got %d", q3.TotalCents)
	}
}

func TestComputeOverageWatermarkDoesNotDoubleCharge(t *testing.T) {
	plan := meteredPlan()
	// 第一次出账：用了 25GB，超 5GB → 5 元，水位推到 25GB。
	first := ComputeOverage(plan, 25*1024, 0, 0, 0)
	if first.TotalCents != 500 {
		t.Fatalf("first charge = %d, want 500", first.TotalCents)
	}
	// 用量没变，再出账必须 0 元。
	same := ComputeOverage(plan, 25*1024, 25*1024, 0, 0)
	if same.TotalCents != 0 {
		t.Fatalf("re-billing the same usage charged %d, want 0", same.TotalCents)
	}
	// 用量涨到 27GB：只对新增的 2GB 收费。
	more := ComputeOverage(plan, 27*1024, 25*1024, 0, 0)
	if more.TotalCents != 200 {
		t.Fatalf("incremental charge = %d, want 200 for the 2 new GB", more.TotalCents)
	}
}

func TestComputeOverageWatermarkBelowQuota(t *testing.T) {
	plan := meteredPlan()
	// 水位还在包含额度以内时，水位不应该把账单抵消掉。
	q := ComputeOverage(plan, 25*1024, 5*1024, 0, 0)
	if q.TotalCents != 500 {
		t.Fatalf("total = %d, want the full 5GB charge (watermark inside quota)", q.TotalCents)
	}
}

func TestComputeOverageBandwidth(t *testing.T) {
	plan := meteredPlan()
	// 流量按整数 GB 计：503GB 用量、500GB 包含 → 3GB × 0.2 元 = 0.6 元 = 60 分。
	q := ComputeOverage(plan, 0, 0, 503, 0)
	if q.TotalCents != 60 {
		t.Fatalf("bandwidth charge = %d, want 60 cents for 3GB", q.TotalCents)
	}
	if len(q.Lines) != 1 || q.Lines[0].Quantity != 3 || q.Lines[0].Resource != "bandwidth" {
		t.Fatalf("unexpected line: %+v", q.Lines)
	}
	// 刚好用完包含额度：不收费。
	if q2 := ComputeOverage(plan, 0, 0, 500, 0); q2.TotalCents != 0 {
		t.Fatalf("usage exactly at the included quota must be free, got %d", q2.TotalCents)
	}
	// 磁盘与流量同时超量：两条明细都要出现。
	q3 := ComputeOverage(plan, 25*1024, 0, 510, 0)
	if len(q3.Lines) != 2 {
		t.Fatalf("expected both resources to be billed, got %+v", q3.Lines)
	}
	if q3.TotalCents != 500+200 {
		t.Fatalf("combined total = %d, want 700", q3.TotalCents)
	}
}

func TestComputeOverageHardLimit(t *testing.T) {
	plan := meteredPlan()
	// 磁盘超硬上限。
	if q := ComputeOverage(plan, 51*1024, 0, 0, 0); !q.OverHardLimit {
		t.Fatal("disk usage over the hard limit must be flagged")
	}
	// 流量超硬上限。
	if q := ComputeOverage(plan, 0, 0, 1001, 0); !q.OverHardLimit {
		t.Fatal("bandwidth usage over the hard limit must be flagged")
	}
	// 刚好等于上限不算超。
	if q := ComputeOverage(plan, 50*1024, 0, 1000, 0); q.OverHardLimit {
		t.Fatal("usage exactly at the hard limit must not be flagged")
	}
}

func TestComputeOverageDisabledAndZeroPrice(t *testing.T) {
	// 未开启按量计费：永远 0 元。
	disabled := meteredPlan()
	disabled.Enabled = false
	if q := ComputeOverage(disabled, 999*1024, 0, 9999, 0); q.TotalCents != 0 || q.OverHardLimit {
		t.Fatalf("disabled metering must not charge or suspend, got %+v", q)
	}
	// 单价为 0（只统计不收钱）：不产生账单，但仍然要能触发硬上限。
	free := meteredPlan()
	free.DiskPricePerGB = 0
	free.BWPricePerGBCents = 0
	q := ComputeOverage(free, 60*1024, 0, 0, 0)
	if q.TotalCents != 0 {
		t.Fatalf("zero-priced metering charged %d", q.TotalCents)
	}
	if !q.OverHardLimit {
		t.Fatal("zero-priced metering must still enforce the hard limit")
	}
}
