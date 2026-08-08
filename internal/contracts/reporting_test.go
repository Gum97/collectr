package contracts

import "testing"

// TestLinkStatsRatesUseTheBreakdownDenominator pins the bug that produced a
// rate of -3.127 in a live response: the network count is taken from raw events
// while Clicks comes from rollups that reach further back, so the two are not
// comparable and a ratio between them can land anywhere.
func TestLinkStatsRatesUseTheBreakdownDenominator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stats      LinkStats
		wantPerNet float64
		wantQR     float64
	}{
		{
			// The live response that exposed the bug: 39 rollup clicks against
			// 161 raw ones, because the rollups reach past the raw retention.
			name: "rollup history exceeds raw retention",
			stats: LinkStats{
				Clicks: 39, BreakdownClicks: 161, Networks: 161,
				Sources: []Breakdown{{Key: "direct", Clicks: 161}},
			},
			wantPerNet: 1,
			wantQR:     0,
		},
		{
			name: "traffic concentrated in a few networks",
			stats: LinkStats{
				Clicks: 900, BreakdownClicks: 200, Networks: 4,
				Sources: []Breakdown{{Key: "qr", Clicks: 50}, {Key: "direct", Clicks: 150}},
			},
			wantPerNet: 50,
			wantQR:     0.25,
		},
		{
			name:       "no clicks in the breakdown window",
			stats:      LinkStats{Clicks: 500, BreakdownClicks: 0, Networks: 0},
			wantPerNet: 0,
			wantQR:     0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.stats.ClicksPerNetwork(); got != tc.wantPerNet {
				t.Errorf("ClicksPerNetwork() = %v, want %v", got, tc.wantPerNet)
			}
			if got := tc.stats.QRShare(); got != tc.wantQR {
				t.Errorf("QRShare() = %v, want %v", got, tc.wantQR)
			}
		})
	}
}

// TestQRShareStaysInRange: a share cannot be negative or above one. A rate
// outside that range on a dashboard is worse than a missing one, because it is
// read as a number rather than as a fault.
func TestQRShareStaysInRange(t *testing.T) {
	t.Parallel()

	cases := []LinkStats{
		{Clicks: 39, BreakdownClicks: 161, Networks: 161, Sources: []Breakdown{{Key: "qr", Clicks: 161}}},
		{Clicks: 0, BreakdownClicks: 10, Networks: 1},
		{Clicks: 1000, BreakdownClicks: 1, Networks: 1, Sources: []Breakdown{{Key: "qr", Clicks: 1}}},
	}
	for _, s := range cases {
		if q := s.QRShare(); q < 0 || q > 1 {
			t.Errorf("QRShare() = %v for %+v, want within [0,1]", q, s)
		}
	}
}
