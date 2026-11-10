package main

import "math"

// Each indicator returns one value per bar, NaN until there are enough bars before it.

func sma(values []float64, n int) []float64 {
	out := nans(len(values))
	sum := 0.0
	for i, v := range values {
		sum += v
		if i >= n {
			sum -= values[i-n]
		}
		if i >= n-1 {
			out[i] = sum / float64(n)
		}
	}
	return out
}

// rsi is Wilder's relative strength index, from 0 to 100.
func rsi(closes []float64, n int) []float64 {
	out := nans(len(closes))
	if len(closes) <= n {
		return out
	}
	var gain, loss float64
	for i := 1; i <= n; i++ {
		change := closes[i] - closes[i-1]
		gain += math.Max(change, 0)
		loss += math.Max(-change, 0)
	}
	gain /= float64(n)
	loss /= float64(n)
	out[n] = rsiValue(gain, loss)
	for i := n + 1; i < len(closes); i++ {
		change := closes[i] - closes[i-1]
		gain = (gain*float64(n-1) + math.Max(change, 0)) / float64(n)
		loss = (loss*float64(n-1) + math.Max(-change, 0)) / float64(n)
		out[i] = rsiValue(gain, loss)
	}
	return out
}

func rsiValue(gain, loss float64) float64 {
	if loss == 0 {
		return 100
	}
	return 100 - 100/(1+gain/loss)
}

// atr is Wilder's average true range, the typical daily move in rupiah.
func atr(bars []Bar, n int) []float64 {
	out := nans(len(bars))
	if len(bars) <= n {
		return out
	}
	trueRange := func(i int) float64 {
		previous := bars[i-1].Close
		return math.Max(bars[i].High-bars[i].Low,
			math.Max(math.Abs(bars[i].High-previous), math.Abs(bars[i].Low-previous)))
	}
	value := 0.0
	for i := 1; i <= n; i++ {
		value += trueRange(i)
	}
	value /= float64(n)
	out[n] = value
	for i := n + 1; i < len(bars); i++ {
		value = (value*float64(n-1) + trueRange(i)) / float64(n)
		out[i] = value
	}
	return out
}

func nans(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

// tick is IDX's price step for a stock trading at this price.
func tick(price float64) float64 {
	switch {
	case price < 200:
		return 1
	case price < 500:
		return 2
	case price < 2000:
		return 5
	case price < 5000:
		return 10
	default:
		return 25
	}
}

// roundDown is the highest valid IDX price at or below this one.
func roundDown(price float64) float64 {
	step := tick(price)
	return math.Floor(price/step+1e-9) * step
}
