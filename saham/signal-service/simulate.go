package main

import (
	"math"
	"time"
)

// IDX costs a typical online broker charges, the sell side including the 0.1% tax.
const (
	buyFee  = 0.0015
	sellFee = 0.0025
)

// Where a signal's trade stands.
const (
	statusWaiting = "waiting" // the next day hasn't traded yet
	statusSkipped = "skipped" // the next open was above MaxEntry or below Stop, so no buy
	statusOpen    = "open"    // bought and still held
	statusTarget  = "target"  // sold at the target
	statusStop    = "stop"    // sold at the stop loss, or at the trailing stop
	statusExit    = "exit"    // sold at the close because the rule's exit came
	statusTime    = "time"    // sold at the close after MaxDays
)

// Outcome is what the trade from a signal did, the same way in the backtest and live.
type Outcome struct {
	Status     string
	EntryDate  time.Time
	EntryPrice float64
	ExitDate   time.Time
	ExitPrice  float64 // for an open trade, the last close
	Stop       float64 // the stop in force after the last bar, which a trailing stop raises
	Days       int     // trading days held
	Return     float64 // after costs, 0.05 is 5%
	ExitIndex  int     // the bar it ended on, or the last bar
}

// Closed reports whether the trade can't change anymore.
func (o Outcome) Closed() bool {
	return o.Status != statusWaiting && o.Status != statusOpen
}

// simulate follows the signal made by bar i of s through the bars after it. When one day
// reaches both the stop and the target, it counts the stop, since daily bars can't tell which
// came first. A trailing stop only uses closes before the day it applies to.
func simulate(s *series, i int, sig Signal) Outcome {
	bars := s.bars
	j := i + 1
	if j >= len(bars) {
		return Outcome{Status: statusWaiting, ExitIndex: len(bars) - 1, Stop: sig.Stop}
	}
	open := bars[j].Open
	if open > sig.MaxEntry || open <= sig.Stop {
		return Outcome{Status: statusSkipped, ExitDate: bars[j].Date, ExitIndex: j}
	}
	out := Outcome{Status: statusOpen, EntryDate: bars[j].Date, EntryPrice: open, Stop: sig.Stop}
	highestClose := 0.0
	for k := j; k < len(bars); k++ {
		b := bars[k]
		out.Days = k - j + 1
		out.ExitIndex = k
		out.ExitDate = b.Date
		switch {
		case b.Low <= out.Stop:
			out.Status = statusStop
			// A gap below the stop sells at the open.
			out.ExitPrice = out.Stop
			if k > j {
				out.ExitPrice = math.Min(b.Open, out.Stop)
			}
		case sig.Target > 0 && b.High >= sig.Target:
			out.Status = statusTarget
			out.ExitPrice = sig.Target
			if k > j {
				out.ExitPrice = math.Max(b.Open, sig.Target)
			}
		case sig.Exit == exitMA5 && b.Close > s.sma5[k]:
			out.Status = statusExit
			out.ExitPrice = b.Close
		case out.Days >= sig.MaxDays:
			out.Status = statusTime
			out.ExitPrice = b.Close
		default:
			out.ExitPrice = b.Close
			if sig.Exit == exitTrail && !math.IsNaN(s.atr14[k]) {
				highestClose = math.Max(highestClose, b.Close)
				out.Stop = math.Max(out.Stop, roundDown(highestClose-sig.TrailATR*s.atr14[k]))
			}
			continue
		}
		break
	}
	out.Return = out.ExitPrice*(1-sellFee)/(out.EntryPrice*(1+buyFee)) - 1
	return out
}
