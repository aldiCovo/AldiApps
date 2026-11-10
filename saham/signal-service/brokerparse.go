package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A broker summary copied as text from Stockbit's web page: each cell on its own line. The
// stock code and period come first, then Stockbit's Top 1/3/5/Average rows, the broker count
// and net figures, and after "BY" and "SL" the table, one broker at a time: code, value, lots,
// average price for a buyer, then the same for a seller.

type brokerLine struct {
	Broker string
	Value  float64 // rupiah
	Lots   float64
	Avg    float64 // rupiah per share
}

type brokerTop struct {
	Name    string  // Top 1, Top 3, Top 5, Average
	Lots    float64 // signed: negative is distribution
	Percent float64
	ValueB  float64 // billions of rupiah, as Stockbit shows it
	Label   string  // such as "Normal Dist"
}

type brokerSummary struct {
	ID         int64
	Ticker     string
	Start, End time.Time
	Investor   string
	Market     string
	Mode       string
	Buyers     *int
	Sellers    *int
	NetLots    *float64
	NetValue   *float64
	AvgPrice   *float64
	AccDist    string
	Top        []brokerTop
	Buy, Sell  []brokerLine
	Pasted     string
	CreatedAt  time.Time
}

var (
	tickerLine = regexp.MustCompile(`^[A-Z]{4}$`)
	brokerCode = regexp.MustCompile(`^[A-Z]{2}$`)
	dateLine   = regexp.MustCompile(`^(\d{1,2}) ([A-Za-z]{3}) (\d{2})$`)
)

// Both English and Indonesian month abbreviations, since the page may show either.
var monthAbbr = map[string]time.Month{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "mei": 5, "jun": 6, "jul": 7,
	"aug": 8, "agu": 8, "agt": 8, "sep": 9, "oct": 10, "okt": 10, "nov": 11, "dec": 12, "des": 12,
}

// parseBrokerSummary reads the pasted text. It needs the stock, the period and at least one
// broker line.
func parseBrokerSummary(text string) (brokerSummary, error) {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		if l = strings.TrimSpace(strings.ReplaceAll(l, " ", " ")); l != "" {
			lines = append(lines, l)
		}
	}
	s := brokerSummary{Pasted: text}
	var dates []time.Time
	tableAt := -1
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		next := func(n int) []string {
			if i+n < len(lines) {
				return lines[i+1 : i+1+n]
			}
			return nil
		}
		switch {
		case s.Ticker == "" && tickerLine.MatchString(l):
			s.Ticker = l
		case len(dates) < 2 && dateLine.MatchString(l):
			d, err := parseStockbitDate(l)
			if err != nil {
				return s, err
			}
			dates = append(dates, d)
		case l == "Top 1" || l == "Top 3" || l == "Top 5" || l == "Average":
			if v := next(4); v != nil {
				lots, err1 := parseAmount(v[0])
				percent, err2 := parseAmount(v[1])
				valueB, err3 := parseAmount(v[2])
				if err1 == nil && err2 == nil && err3 == nil {
					s.Top = append(s.Top, brokerTop{Name: l, Lots: lots, Percent: percent, ValueB: valueB, Label: v[3]})
					i += 4
				}
			}
		case l == "Broker":
			if v := next(4); v != nil {
				b, err1 := strconv.Atoi(v[0])
				se, err2 := strconv.Atoi(v[1])
				if err1 == nil && err2 == nil {
					s.Buyers, s.Sellers, s.AccDist = &b, &se, v[3]
					i += 4
				}
			}
		case l == "Net Volume" || l == "Net Value" || l == "Average (Rp)":
			if v := next(1); v != nil {
				if f, err := parseAmount(v[0]); err == nil {
					switch l {
					case "Net Volume":
						s.NetLots = &f
					case "Net Value":
						s.NetValue = &f
					default:
						s.AvgPrice = &f
					}
					i++
				}
			}
		case l == "SL" && tableAt < 0:
			tableAt = i + 1
		}
		if tableAt >= 0 {
			break
		}
	}
	if s.Ticker == "" {
		return s, errors.New("kode saham (4 huruf, seperti BBRI) tidak ditemukan")
	}
	if len(dates) == 0 {
		return s, errors.New("periode (seperti 01 Nov 26) tidak ditemukan")
	}
	s.Start, s.End = dates[0], dates[0]
	if len(dates) == 2 {
		s.End = dates[1]
	}
	if s.End.Before(s.Start) {
		s.Start, s.End = s.End, s.Start
	}
	if tableAt < 0 {
		return s, errors.New(`tabel broker (setelah "BY" dan "SL") tidak ditemukan`)
	}
	if err := s.readTable(lines[tableAt:]); err != nil {
		return s, err
	}
	if len(s.Buy)+len(s.Sell) == 0 {
		return s, errors.New("tidak ada baris broker yang terbaca")
	}
	return s, nil
}

// readTable reads the broker lines. They come in pairs, buyer then seller. Once one side runs
// out, Stockbit fills its cells with "-", which keeps the pairs exact. Without those dashes
// (an older copy, or a cut one) the leftover lines are placed by value: each side is sorted
// biggest first, so a line bigger than that side's last can't be on it.
func (s *brokerSummary) readTable(lines []string) error {
	var entries []*brokerLine // nil is an empty cell
	dashes := false
	for i := 0; i < len(lines); {
		if lines[i] == "-" {
			if i+3 >= len(lines) || lines[i+1] != "-" || lines[i+2] != "-" || lines[i+3] != "-" {
				return errors.New(`sel kosong harus empat tanda "-"`)
			}
			entries = append(entries, nil)
			dashes = true
			i += 4
			continue
		}
		if !brokerCode.MatchString(lines[i]) {
			return fmt.Errorf("baris %q tidak terbaca sebagai kode broker", lines[i])
		}
		if i+3 >= len(lines) {
			return fmt.Errorf("broker %s tidak punya value, lot dan rata-rata lengkap", lines[i])
		}
		value, err1 := parseAmount(lines[i+1])
		lots, err2 := parseAmount(lines[i+2])
		avg, err3 := parseAmount(lines[i+3])
		if err := errors.Join(err1, err2, err3); err != nil {
			return fmt.Errorf("broker %s: %w", lines[i], err)
		}
		entries = append(entries, &brokerLine{Broker: lines[i], Value: value, Lots: lots, Avg: avg})
		i += 4
	}
	if dashes {
		for k, e := range entries {
			switch {
			case e == nil:
			case k%2 == 0:
				s.Buy = append(s.Buy, *e)
			default:
				s.Sell = append(s.Sell, *e)
			}
		}
		return nil
	}
	wantBuy := true
	for _, e := range entries {
		buyFits := len(s.Buy) == 0 || e.Value <= s.Buy[len(s.Buy)-1].Value
		sellFits := len(s.Sell) == 0 || e.Value <= s.Sell[len(s.Sell)-1].Value
		buy := wantBuy
		if wantBuy && !buyFits && sellFits {
			buy = false
		} else if !wantBuy && !sellFits && buyFits {
			buy = true
		}
		if buy {
			s.Buy = append(s.Buy, *e)
		} else {
			s.Sell = append(s.Sell, *e)
		}
		wantBuy = !buy
	}
	return nil
}

func parseStockbitDate(l string) (time.Time, error) {
	m := dateLine.FindStringSubmatch(l)
	month, ok := monthAbbr[strings.ToLower(m[2])]
	if !ok {
		return time.Time{}, fmt.Errorf("bulan %q tidak dikenal", m[2])
	}
	day, _ := strconv.Atoi(m[1])
	year, _ := strconv.Atoi(m[3])
	d := time.Date(2000+year, month, day, 0, 0, 0, 0, wib)
	if d.Day() != day {
		return time.Time{}, fmt.Errorf("tanggal %q tidak ada", l)
	}
	return d, nil
}

// parseAmount reads Stockbit's numbers: "2,761", "-463,505", "-18.8", "233.4B", "846.3K",
// "982.2M", "1.3M". Commas separate thousands and the dot is the decimal point.
func parseAmount(v string) (float64, error) {
	v = strings.ReplaceAll(strings.TrimSpace(v), ",", "")
	scale := 1.0
	switch {
	case strings.HasSuffix(v, "T"):
		scale, v = 1e12, strings.TrimSuffix(v, "T")
	case strings.HasSuffix(v, "B"):
		scale, v = 1e9, strings.TrimSuffix(v, "B")
	case strings.HasSuffix(v, "M"):
		scale, v = 1e6, strings.TrimSuffix(v, "M")
	case strings.HasSuffix(v, "K"):
		scale, v = 1e3, strings.TrimSuffix(v, "K")
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(v, "−", "-"), 64)
	if err != nil {
		return 0, fmt.Errorf("angka %q tidak terbaca", v)
	}
	return f * scale, nil
}

// brokerAnalysis is what the pages show for a summary, with the foreign brokers known.
type brokerAnalysis struct {
	ForeignBuy, ForeignSell float64 // rupiah
	LocalBuy, LocalSell     float64
	TotalBuy, TotalSell     float64
	ForeignBuyers           []brokerLine
	ForeignSellers          []brokerLine
	Top3BuyShare            float64 // the three biggest buyers' share of the buying shown
	Top3SellShare           float64
	Price                   float64 // the last close, NaN when unknown
	PriceDay                time.Time
}

func (a brokerAnalysis) ForeignNet() float64 { return a.ForeignBuy - a.ForeignSell }
func (a brokerAnalysis) LocalNet() float64   { return a.LocalBuy - a.LocalSell }

func analyzeBrokers(s brokerSummary, foreign map[string]bool) brokerAnalysis {
	var a brokerAnalysis
	for _, l := range s.Buy {
		a.TotalBuy += l.Value
		if foreign[l.Broker] {
			a.ForeignBuy += l.Value
			a.ForeignBuyers = append(a.ForeignBuyers, l)
		} else {
			a.LocalBuy += l.Value
		}
	}
	for _, l := range s.Sell {
		a.TotalSell += l.Value
		if foreign[l.Broker] {
			a.ForeignSell += l.Value
			a.ForeignSellers = append(a.ForeignSellers, l)
		} else {
			a.LocalSell += l.Value
		}
	}
	a.Top3BuyShare = topShare(s.Buy, a.TotalBuy)
	a.Top3SellShare = topShare(s.Sell, a.TotalSell)
	return a
}

func topShare(lines []brokerLine, total float64) float64 {
	if total == 0 {
		return 0
	}
	sum := 0.0
	for _, l := range lines[:min(3, len(lines))] {
		sum += l.Value
	}
	return sum / total
}

// samePaste reports whether two pasted texts hold the same cells, whatever the blank lines and
// spaces around them.
func samePaste(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}
