package store

import "testing"

// NOKVM 实例数据（main_ip + assigned_ips）按参考插件语义分流成主 IP / 分配 IP。
func TestExpiredIPsFromPayload(t *testing.T) {
	ded, asg := expiredIPsFromPayload(map[string]any{"main_ip": "1.2.3.4", "assigned_ips": []any{"5.6.7.8", "9.9.9.9"}})
	if ded != "1.2.3.4" || asg != "5.6.7.8,9.9.9.9" {
		t.Fatalf("nokvm payload -> %q / %q", ded, asg)
	}

	ded, asg = expiredIPsFromPayload(map[string]any{"dedicatedip": "10.0.0.1", "assignedips": "10.0.0.2,10.0.0.3"})
	if ded != "10.0.0.1" || asg != "10.0.0.2,10.0.0.3" {
		t.Fatalf("plugin-style keys -> %q / %q", ded, asg)
	}

	ded, asg = expiredIPsFromPayload(map[string]any{"ip": []string{"1.1.1.1"}})
	if ded != "1.1.1.1" || asg != "" {
		t.Fatalf("aliases -> %q / %q", ded, asg)
	}

	// 空值 / 无关字段不产生脏数据。
	for _, data := range []map[string]any{nil, {}, {"mode": "manual"}, {"assigned_ips": []any{}}} {
		ded, asg = expiredIPsFromPayload(data)
		if ded != "" || asg != "" {
			t.Fatalf("empty payload %v -> %q / %q", data, ded, asg)
		}
	}
}
