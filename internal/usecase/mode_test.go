package usecase

import "testing"

func TestHostChildSlots(t *testing.T) {
	cases := []struct {
		name      string
		uploadBps int64
		streamBps int64
		want      int
	}{
		{"未实测上行时保守默认", 0, 2_000_000, 2},
		{"未实测码率时保守默认", 12_000_000, 0, 2},
		{"12Mbps 上行 / 2Mbps 码率", 12_000_000, 2_000_000, 4},
		{"刚好 2 倍码率", 4_000_000, 2_000_000, 1},
		{"低于 2 倍码率", 3_000_000, 2_000_000, 1},
		{"低于 1.25 倍码率则一个都带不动", 2_000_000, 2_000_000, 0},
		{"码率远小于上行时受硬上限约束", 100_000_000, 1_000_000, MaxChildren},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HostChildSlots(tc.uploadBps, tc.streamBps); got != tc.want {
				t.Fatalf("HostChildSlots(%d, %d) = %d，期望 %d", tc.uploadBps, tc.streamBps, got, tc.want)
			}
		})
	}
}

func TestSelectMode(t *testing.T) {
	cases := map[int]Mode{
		0: ModeChain,
		1: ModeChain,
		2: ModeFanout,
		4: ModeFanout,
	}
	for slots, want := range cases {
		if got := SelectMode(slots); got != want {
			t.Fatalf("SelectMode(%d) = %q，期望 %q", slots, got, want)
		}
	}
}

func TestCanDistribute(t *testing.T) {
	const stream = int64(2_000_000)

	cases := []struct {
		name        string
		capacityBps int64
		memberCount int
		stability   float64
		want        bool
	}{
		// 4 个观众需要 (4-1)*2Mbps/0.8 = 7.5 Mbps
		{"容量刚好够", 7_500_000, 4, 0.9, true},
		{"容量略不足", 7_000_000, 4, 0.9, false},
		{"稳定性不达标", 20_000_000, 4, 0.5, false},
		{"只有自己时无需分发", 20_000_000, 1, 1, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanDistribute(tc.capacityBps, tc.memberCount, stream, tc.stability); got != tc.want {
				t.Fatalf("CanDistribute(%d, %d, %v) = %v，期望 %v",
					tc.capacityBps, tc.memberCount, tc.stability, got, tc.want)
			}
		})
	}
}
