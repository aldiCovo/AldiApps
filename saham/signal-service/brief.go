package main

import (
	"fmt"
	"html"
	"math"
	"strings"
	"time"
)

// maxScreener keeps the brief within Telegram's 4096 characters on a busy day.
const maxScreener = 12

var bulan = []string{"", "Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}

// composeBrief writes the day's Telegram message, as Telegram's HTML.
func composeBrief(r dayReport) string {
	var b strings.Builder
	m := r.Market
	fmt.Fprintf(&b, "📊 <b>Ringkasan pasar %s</b>\n\n", tanggal(r.Day))
	fmt.Fprintf(&b, "IHSG %s (%s hari ini, %s dalam 20 hari)\n", rp(m.Close), pct(m.Change1d), pct(m.Change20d))
	switch {
	case m.Healthy && m.MA50 > m.MA200:
		b.WriteString("Kondisi: 🟢 <b>SEHAT</b>, di atas MA200 dan MA50 di atas MA200\n")
	case m.Healthy:
		b.WriteString("Kondisi: 🟡 <b>WASPADA</b>, di atas MA200 tapi MA50 masih di bawahnya\n")
	default:
		b.WriteString("Kondisi: 🔴 <b>LEMAH</b>, di bawah MA200\n")
	}
	fmt.Fprintf(&b, "MA50 %s · MA200 %s\n", rp(m.MA50), rp(m.MA200))
	if m.Counted > 0 {
		fmt.Fprintf(&b, "Saham LQ45 di atas MA200: %d dari %d (%.0f%%)\n", m.Above200, m.Counted, float64(m.Above200)/float64(m.Counted)*100)
	}

	b.WriteString("\n🔎 <b>Screener</b> <i>(belum terbukti menguntungkan, bukan rekomendasi)</i>\n")
	if len(r.Screener) == 0 {
		b.WriteString("Tidak ada saham yang memenuhi aturan hari ini.\n")
	}
	for i, s := range r.Screener {
		if i == maxScreener {
			fmt.Fprintf(&b, "…dan %d lainnya\n", len(r.Screener)-maxScreener)
			break
		}
		fmt.Fprintf(&b, "• <b>%s</b> %s: %s\n  Beli maks %s · SL %s", s.Ticker, s.Strategy,
			html.EscapeString(strings.Join(s.Reasons, "; ")), rp(s.MaxEntry), rp(s.Stop))
		switch s.Exit {
		case exitTarget:
			fmt.Fprintf(&b, " · Target %s", rp(s.Target))
		case exitMA5:
			b.WriteString(" · jual saat close di atas MA5")
		case exitTrail:
			b.WriteString(" · trailing stop")
		}
		b.WriteString("\n")
	}

	if len(r.Bought) > 0 || len(r.Sold) > 0 {
		b.WriteString("\n📒 <b>Paper trading hari ini</b>\n")
		for _, e := range r.Bought {
			fmt.Fprintf(&b, "• Beli %s (%s) di %s\n", e.Signal.Ticker, e.Signal.Strategy, rp(e.Outcome.EntryPrice))
		}
		for _, e := range r.Sold {
			fmt.Fprintf(&b, "• Jual %s (%s), %s: %s setelah %d hari\n", e.Signal.Ticker, e.Signal.Strategy,
				exitName(e.Outcome.Status), pct(e.Outcome.Return), e.Outcome.Days)
		}
	}

	fmt.Fprintf(&b, "\n📈 <b>Hasil paper trading sejak %s</b>\n<pre>", tanggal(r.Started))
	b.WriteString(statsTable(r.Stats))
	b.WriteString("</pre>\n")
	b.WriteString("<i>Selesai = trade yang sudah dijual. Rata2 per trade setelah biaya. Aturan berguna kalau lebih baik dari acak dengan cara keluar yang sama.</i>\n")

	mo := r.Momentum
	b.WriteString("\n🏆 <b>Momentum bulanan</b> (5 saham terkuat 6 bulan, ganti tiap akhir bulan)\n")
	switch {
	case !mo.HasMonth:
		b.WriteString("Mulai di akhir bulan ini.\n")
	case !mo.MarketOK:
		fmt.Fprintf(&b, "Sejak %s: kas, karena IHSG di bawah MA200.", tanggal(mo.Since))
		if mo.Months > 0 {
			fmt.Fprintf(&b, " Total %s dalam %d bulan selesai.", pct(mo.Total), mo.Months)
		}
		b.WriteString("\n")
	default:
		fmt.Fprintf(&b, "Sejak %s: %s · %s bulan ini", tanggal(mo.Since), strings.Join(mo.Current, ", "), pct(mo.Running))
		if mo.Months > 0 {
			fmt.Fprintf(&b, " · total %s dalam %d bulan selesai", pct(mo.Total), mo.Months)
		}
		b.WriteString("\n")
	}

	if len(r.Missing) > 0 {
		fmt.Fprintf(&b, "\n⚠️ Tanpa data dari Yahoo: %s\n", strings.Join(r.Missing, ", "))
	}
	if len(r.Suspended) > 0 {
		fmt.Fprintf(&b, "⚠️ Tidak diperdagangkan hari ini: %s\n", strings.Join(r.Suspended, ", "))
	}
	return b.String()
}

// statsTable lines each rule up with its random comparison, for all days and healthy days.
func statsTable(stats []ruleStats) string {
	type key struct {
		rule string
		ok   string
	}
	byKey := map[key]ruleStats{}
	for _, s := range stats {
		k := key{s.Rule, "all"}
		if s.MarketOK != nil {
			k.ok = fmt.Sprint(*s.MarketOK)
		}
		byKey[k] = s
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-15s %7s %6s %7s %5s\n", "Aturan", "Selesai", "Menang", "Rata2", "Buka")
	rows := []string{
		strategyPull, strategyBrk, strategyBasePrefx + strategyBrk,
		strategyRSI2, strategyBasePrefx + strategyRSI2,
		strategyBrkTrail, strategyBasePrefx + strategyBrkTrail,
	}
	for _, rule := range rows {
		s := byKey[key{rule, "all"}]
		fmt.Fprintf(&b, "%-15s %7d %6s %7s %5d\n", shortRule(rule), s.Closed, winRate(s), meanPct(s), s.Open)
	}
	b.WriteString("\nSaat IHSG di atas MA200:\n")
	for _, rule := range rows {
		s := byKey[key{rule, "true"}]
		fmt.Fprintf(&b, "%-15s %7d %6s %7s %5d\n", shortRule(rule), s.Closed, winRate(s), meanPct(s), s.Open)
	}
	return html.EscapeString(b.String())
}

func shortRule(rule string) string {
	return strings.Replace(rule, "breakout-trail", "brk-trail", 1)
}

func winRate(s ruleStats) string {
	if s.Closed == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", float64(s.Wins)/float64(s.Closed)*100)
}

func meanPct(s ruleStats) string {
	if s.Closed == 0 {
		return "-"
	}
	return pct(s.MeanPct / 100)
}

func exitName(status string) string {
	switch status {
	case statusTarget:
		return "kena target"
	case statusStop:
		return "kena stop"
	case statusExit:
		return "sinyal keluar"
	case statusTime:
		return "batas waktu"
	}
	return status
}

// tanggal writes a day the Indonesian way, such as "10 Nov 2026".
func tanggal(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), bulan[t.Month()], t.Year())
}

// rp writes a price in whole rupiah with dots between thousands, such as "7.123".
func rp(price float64) string {
	if math.IsNaN(price) {
		return "-"
	}
	n := int64(math.Round(price))
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	digits := fmt.Sprint(n)
	var out []byte
	for i := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, digits[i])
	}
	return sign + string(out)
}

// pct writes a fraction as a signed percentage with a decimal comma, such as "+1,2%".
func pct(f float64) string {
	if math.IsNaN(f) {
		return "-"
	}
	s := fmt.Sprintf("%+.1f%%", f*100)
	s = strings.Replace(s, ".", ",", 1)
	if s == "-0,0%" {
		s = "+0,0%"
	}
	return strings.Replace(s, "-", "−", 1)
}
