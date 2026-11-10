package main

import (
	"fmt"
	"math"
	"slices"
	"time"
)

// A signal comes after the close; the trade is bought at the next day's open, unless that open
// is too far above the close or already below the stop.
const (
	maxChase   = 1.02 // don't buy if the next open is more than 2% above the close
	warmupBars = 200  // MA200 needs this many bars
)

// The rules. Their numbers are the usual ones from the trading literature, fixed before any
// backtest, so the results aren't fitted to the past.
const (
	strategyPull      = "pullback"       // RSI(14) turns up in an uptrend, fixed target
	strategyBrk       = "breakout"       // 20-day high on volume, fixed target
	strategyRSI2      = "rsi2"           // RSI(2) below 10 in an uptrend, out above MA5
	strategyBrkTrail  = "breakout-trail" // 20-day high on volume, trailing stop
	strategyMomentum  = "momentum"       // monthly: the 5 strongest stocks over 6 months
	strategyBasePrefx = "acak/"          // random entries with a rule's exits, to compare
)

// How a trade gets out, besides its stop loss and its day limit.
const (
	exitTarget = "target" // at a fixed target
	exitTrail  = "trail"  // when the stop, trailing the highest close, is hit
	exitMA5    = "ma5"    // at the close, once it is above MA5
)

// Signal is a stock to buy at the next open, with where to get out.
type Signal struct {
	Ticker   string
	Strategy string
	Date     time.Time // the day whose close made the signal
	Close    float64
	MaxEntry float64 // don't buy above this
	Stop     float64
	Target   float64 // 0 when the exit has no fixed target
	Exit     string
	TrailATR float64 // for exitTrail, how many ATRs below the highest close the stop follows
	MaxDays  int     // sell at the close after this many trading days
	Reasons  []string
}

// series holds a stock's bars with the indicators the rules read.
type series struct {
	ticker   string
	bars     []Bar
	closes   []float64
	sma5     []float64
	sma50    []float64
	sma200   []float64
	rsi2     []float64
	rsi14    []float64
	atr14    []float64
	avgVol20 []float64
	// market says, by "2006-01-02", whether IHSG allows buying that day. Nil allows every day.
	market map[string]bool
	// members gives the stocks in the index on a day. Nil counts the stock as always in it.
	members func(time.Time) []string
}

// inIndex reports whether the stock was in the index on the day of bar i.
func (s *series) inIndex(i int) bool {
	return s.members == nil || slices.Contains(s.members(s.bars[i].Date), s.ticker)
}

// Market filters: buy only when IHSG itself is healthy.
const (
	marketAny   = "none"  // no filter
	marketMA200 = "ma200" // IHSG closes above its MA200
)

// marketFilter reads IHSG's bars into the days a filter allows buying.
func marketFilter(index []Bar, mode string) map[string]bool {
	if mode == marketAny {
		return nil
	}
	s := analyze(ihsg, index)
	allowed := map[string]bool{}
	for i, b := range index {
		if !math.IsNaN(s.sma200[i]) {
			allowed[b.Date.Format(time.DateOnly)] = b.Close > s.sma200[i]
		}
	}
	return allowed
}

// marketAllows reports whether the market filter allows buying after bar i.
func (s *series) marketAllows(i int) bool {
	return s.market == nil || s.market[s.bars[i].Date.Format(time.DateOnly)]
}

func analyze(ticker string, bars []Bar) *series {
	closes := make([]float64, len(bars))
	volumes := make([]float64, len(bars))
	for i, b := range bars {
		closes[i] = b.Close
		volumes[i] = float64(b.Volume)
	}
	return &series{
		ticker:   ticker,
		bars:     bars,
		closes:   closes,
		sma5:     sma(closes, 5),
		sma50:    sma(closes, 50),
		sma200:   sma(closes, 200),
		rsi2:     rsi(closes, 2),
		rsi14:    rsi(closes, 14),
		atr14:    atr(bars, 14),
		avgVol20: sma(volumes, 20),
	}
}

// signalsAt returns the signals the close of bar i makes.
func (s *series) signalsAt(i int) []Signal {
	if i < warmupBars || i < 1 {
		return nil
	}
	b := s.bars[i]
	close, ma50, ma200, a := b.Close, s.sma50[i], s.sma200[i], s.atr14[i]
	if math.IsNaN(ma200) || math.IsNaN(a) || a <= 0 || !s.marketAllows(i) {
		return nil
	}
	var out []Signal

	// Pullback: an uptrend dipped until RSI(14) fell below 40, and today turned up.
	if close > ma200 && ma50 > ma200 && s.rsi14[i-1] < 40 && s.rsi14[i] >= 40 && close > b.Open {
		out = append(out, s.signal(i, strategyPull,
			"Tren naik: harga di atas MA200 dan MA50 di atas MA200",
			fmt.Sprintf("RSI(14) naik dari %.0f ke %.0f setelah koreksi", s.rsi14[i-1], s.rsi14[i]),
			"Candle hari ini naik (close > open)"))
	}

	// RSI(2): a stock above its MA200 that fell hard for a day or two tends to bounce.
	if close > ma200 && s.rsi2[i] < 10 {
		out = append(out, s.signal(i, strategyRSI2,
			"Harga di atas MA200",
			fmt.Sprintf("RSI(2) %.0f: turun tajam 1–2 hari, biasanya memantul", s.rsi2[i])))
	}

	// Breakout: the close beats the last 20 days' high on heavy volume, in an uptrend.
	if close > ma200 && close > ma50 && !math.IsNaN(s.avgVol20[i-1]) {
		highest := 0.0
		for j := i - 20; j < i; j++ {
			highest = math.Max(highest, s.bars[j].High)
		}
		volumeRatio := float64(b.Volume) / s.avgVol20[i-1]
		strongClose := b.High > b.Low && (close-b.Low)/(b.High-b.Low) >= 0.6
		if close > highest && volumeRatio >= 1.5 && strongClose {
			reasons := []string{
				fmt.Sprintf("Close %s menembus high 20 hari %s", rupiah(close), rupiah(highest)),
				fmt.Sprintf("Volume %.1fx rata-rata 20 hari", volumeRatio),
				"Harga di atas MA50 dan MA200, close dekat high hari ini",
			}
			out = append(out, s.signal(i, strategyBrk, reasons...), s.signal(i, strategyBrkTrail, reasons...))
		}
	}
	return out
}

// signal sets the entry and exits of a rule for the close of bar i. A random entry made to
// compare against a rule gets that rule's exits.
func (s *series) signal(i int, strategy string, reasons ...string) Signal {
	close, a := s.bars[i].Close, s.atr14[i]
	sig := Signal{
		Ticker:   s.ticker,
		Strategy: strategy,
		Date:     s.bars[i].Date,
		Close:    close,
		MaxEntry: roundDown(close * maxChase),
		Reasons:  reasons,
	}
	rule := strategy
	if len(rule) > len(strategyBasePrefx) && rule[:len(strategyBasePrefx)] == strategyBasePrefx {
		rule = rule[len(strategyBasePrefx):]
	}
	switch rule {
	case strategyRSI2:
		sig.Stop = roundDown(close - 3*a)
		sig.Exit = exitMA5
		sig.MaxDays = 10
	case strategyBrkTrail:
		sig.Stop = roundDown(close - 3*a)
		sig.Exit = exitTrail
		sig.TrailATR = 3
		sig.MaxDays = 40
	default: // pullback and breakout
		sig.Stop = roundDown(close - 2*a)
		sig.Target = roundDown(close + 3*a)
		sig.Exit = exitTarget
		sig.MaxDays = 20
	}
	return sig
}

func rupiah(price float64) string {
	return fmt.Sprintf("%.0f", price)
}
