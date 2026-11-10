package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// wib is Jakarta time. A fixed zone, because the distroless image has no time zone files.
var wib = time.FixedZone("WIB", 7*3600)

// Bar is one trading day of a stock, in rupiah per share and shares traded.
type Bar struct {
	Date   time.Time // midnight WIB of the trading day
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume int64
}

// marketDone is when a day's bar is final. IDX closes at 16:00 and Yahoo can lag a little.
const marketDone = 16*time.Hour + 30*time.Minute

type yahooClient struct {
	http *http.Client
	base string
}

func newYahooClient() *yahooClient {
	return &yahooClient{
		http: &http.Client{Timeout: 20 * time.Second},
		base: "https://query1.finance.yahoo.com",
	}
}

type chartResponse struct {
	Chart struct {
		Result []struct {
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open   []*float64 `json:"open"`
					High   []*float64 `json:"high"`
					Low    []*float64 `json:"low"`
					Close  []*float64 `json:"close"`
					Volume []*float64 `json:"volume"`
				} `json:"quote"`
				AdjClose []struct {
					AdjClose []*float64 `json:"adjclose"`
				} `json:"adjclose"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// ihsg is the Jakarta Composite Index on Yahoo.
const ihsg = "^JKSE"

// Daily returns the finished daily bars of an IDX stock, such as "BBCA", or of ihsg, from one
// day to another. They are adjusted for splits and dividends, so a big dividend doesn't look
// like a crash: the latest bar has the real prices, and older ones are scaled down. Days
// without trades and today's bar before the market is done are left out.
func (c *yahooClient) Daily(ctx context.Context, ticker string, from, to time.Time) ([]Bar, error) {
	symbol := ticker + ".JK"
	if strings.HasPrefix(ticker, "^") {
		symbol = ticker
	}
	address := fmt.Sprintf("%s/v8/finance/chart/%s?period1=%d&period2=%d&interval=1d",
		c.base, url.PathEscape(symbol), from.Unix(), to.Unix())
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 3 * time.Second):
			}
		}
		bars, retry, err := c.fetch(ctx, address, strings.HasPrefix(ticker, "^"))
		if err == nil {
			return bars, nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return nil, fmt.Errorf("prices of %s: %w", ticker, lastErr)
}

func (c *yahooClient) fetch(ctx context.Context, address string, index bool) (bars []Bar, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, false, err
	}
	// Yahoo refuses Go's default client name.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; saham-signal/1.0)")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retry, fmt.Errorf("yahoo answered %d: %.200s", resp.StatusCode, body)
	}
	var chart chartResponse
	if err := json.Unmarshal(body, &chart); err != nil {
		return nil, false, fmt.Errorf("reading yahoo's answer: %w", err)
	}
	if chart.Chart.Error != nil {
		return nil, false, fmt.Errorf("yahoo: %s: %s", chart.Chart.Error.Code, chart.Chart.Error.Description)
	}
	if len(chart.Chart.Result) == 0 || len(chart.Chart.Result[0].Indicators.Quote) == 0 {
		return nil, false, nil
	}
	result := chart.Chart.Result[0]
	quote := result.Indicators.Quote[0]
	var adjusted []*float64
	if len(result.Indicators.AdjClose) > 0 {
		adjusted = result.Indicators.AdjClose[0].AdjClose
	}
	now := time.Now().In(wib)
	today := dayOf(now)
	for i, ts := range result.Timestamp {
		if i >= len(quote.Close) || quote.Open[i] == nil || quote.High[i] == nil ||
			quote.Low[i] == nil || quote.Close[i] == nil || (!index && (quote.Volume[i] == nil || *quote.Volume[i] <= 0)) {
			continue
		}
		day := dayOf(time.Unix(ts, 0))
		if day.Equal(today) && now.Sub(today) < marketDone {
			continue
		}
		// Yahoo sometimes sends the same day twice, the later one being newer.
		if n := len(bars); n > 0 && bars[n-1].Date.Equal(day) {
			bars = bars[:n-1]
		}
		factor := 1.0
		if i < len(adjusted) && adjusted[i] != nil && *quote.Close[i] > 0 {
			factor = *adjusted[i] / *quote.Close[i]
		}
		bars = append(bars, Bar{
			Date:   day,
			Open:   *quote.Open[i] * factor,
			High:   *quote.High[i] * factor,
			Low:    *quote.Low[i] * factor,
			Close:  *quote.Close[i] * factor,
			Volume: volume(quote.Volume[i]),
		})
	}
	return bars, false, nil
}

// dayOf is midnight WIB of the day t falls on in Jakarta.
func dayOf(t time.Time) time.Time {
	y, m, d := t.In(wib).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, wib)
}

func volume(v *float64) int64 {
	if v == nil {
		return 0
	}
	return int64(*v)
}
