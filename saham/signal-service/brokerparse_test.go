package main

import (
	"math"
	"os"
	"testing"
	"time"
)

func TestParseBrokerSummary(t *testing.T) {
	text, err := os.ReadFile("testdata/broker_summary.txt")
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseBrokerSummary(string(text))
	if err != nil {
		t.Fatal(err)
	}
	if s.Ticker != "TEST" || !s.Start.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, wib)) || !s.End.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, wib)) {
		t.Errorf("stock %s, period %v – %v", s.Ticker, s.Start, s.End)
	}
	if len(s.Top) != 4 || s.Top[1].Name != "Top 3" || s.Top[1].Lots != -120500 || s.Top[1].ValueB != -30.2 || s.Top[1].Label != "Normal Dist" {
		t.Errorf("top rows %+v", s.Top)
	}
	if s.Buyers == nil || *s.Buyers != 5 || *s.Sellers != 3 || s.AccDist != "Dist" {
		t.Errorf("buyers %v sellers %v label %q", s.Buyers, s.Sellers, s.AccDist)
	}
	if *s.NetLots != 1234567 || *s.NetValue != 300.5e9 || *s.AvgPrice != 2500 {
		t.Errorf("net lots %v value %v avg %v", *s.NetLots, *s.NetValue, *s.AvgPrice)
	}
	// Three pairs, then BK and XL only on the buying side. BK (5B) can't be a seller, since the
	// sellers' last, AK, is only 10.2M and each side is sorted biggest first.
	wantBuy := []string{"ZP", "OD", "CC", "BK", "XL"}
	wantSell := []string{"KZ", "YU", "AK"}
	if got := codes(s.Buy); !equal(got, wantBuy) {
		t.Errorf("buyers %v, want %v", got, wantBuy)
	}
	if got := codes(s.Sell); !equal(got, wantSell) {
		t.Errorf("sellers %v, want %v", got, wantSell)
	}
	if s.Buy[0].Value != 120e9 || s.Buy[0].Lots != 480e3 || s.Buy[0].Avg != 2510 || s.Sell[2].Value != 10.2e6 || s.Buy[4].Value != 982.2e6 {
		t.Errorf("amounts %+v %+v %+v", s.Buy[0], s.Sell[2], s.Buy[4])
	}

	a := analyzeBrokers(s, map[string]bool{"ZP": true, "KZ": true, "YU": true, "AK": true, "BK": true})
	if math.Abs(a.ForeignBuy-125e9) > 1 || math.Abs(a.ForeignSell-240.0102e9) > 1 {
		t.Errorf("foreign buy %v sell %v", a.ForeignBuy, a.ForeignSell)
	}
	if math.Abs(a.ForeignNet()-(125e9-240.0102e9)) > 1 || math.Abs(a.LocalNet()-(71.4822e9)) > 1 {
		t.Errorf("foreign net %v local net %v", a.ForeignNet(), a.LocalNet())
	}
}

func TestParseBrokerSummaryErrors(t *testing.T) {
	for name, text := range map[string]string{
		"empty":     "",
		"no stock":  "01 Nov 26\nBY\nSL\nZP\n1B\n1K\n100\n",
		"no period": "BBRI\nBY\nSL\nZP\n1B\n1K\n100\n",
		"no table":  "BBRI\n01 Nov 26\n",
		"bad line":  "BBRI\n01 Nov 26\nSL\nZP\n1B\nxx\n100\n",
		"cut short": "BBRI\n01 Nov 26\nSL\nZP\n1B\n",
		"bad month": "BBRI\n01 Abc 26\nSL\nZP\n1B\n1K\n100\n",
	} {
		if _, err := parseBrokerSummary(text); err == nil {
			t.Errorf("%s: read without an error", name)
		}
	}
}

func TestParseAmount(t *testing.T) {
	for in, want := range map[string]float64{"2,761": 2761, "-463,505": -463505, "-18.8": -18.8, "233.4B": 233.4e9,
		"846.3K": 846.3e3, "982.2M": 982.2e6, "1.3M": 1.3e6, "1.2T": 1.2e12, "858": 858} {
		if got, err := parseAmount(in); err != nil || math.Abs(got-want) > 1e-6*math.Abs(want) {
			t.Errorf("parseAmount(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if got := miliar(-287.16e9); got != "−287,2 M" {
		t.Errorf("miliar = %q", got)
	}
}

func codes(lines []brokerLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Broker)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
