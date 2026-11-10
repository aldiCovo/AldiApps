// Draws the price charts. Each element with data-chart gets that stock's candles, moving
// averages, volume and paper-trade markers from /data/<ticker>; data-rsi names the element
// for its RSI(14).
(function () {
  "use strict";
  if (!window.LightweightCharts) return;
  var LC = window.LightweightCharts;
  var css = getComputedStyle(document.documentElement);
  function color(name) { return css.getPropertyValue(name).trim(); }
  var rupiah = function (p) { return Math.round(p).toLocaleString("id-ID"); };

  function base(el) {
    return LC.createChart(el, {
      autoSize: true,
      layout: { background: { color: "transparent" }, textColor: color("--muted"), fontSize: 11 },
      grid: { vertLines: { color: color("--grid") }, horzLines: { color: color("--grid") } },
      rightPriceScale: { borderColor: color("--line"), minimumWidth: 64 },
      timeScale: { borderColor: color("--line") },
      crosshair: { mode: LC.CrosshairMode.Normal },
      localization: { locale: "id-ID", priceFormatter: rupiah },
      handleScroll: { vertTouchDrag: false },
    });
  }

  function line(chart, data, name, width) {
    var s = chart.addLineSeries({ color: color(name), lineWidth: width || 1, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false });
    s.setData(data);
    return s;
  }

  function draw(el) {
    fetch("/data/" + encodeURIComponent(el.dataset.chart), { credentials: "same-origin" })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
      .then(function (d) {
        var chart = base(el);
        var candles = chart.addCandlestickSeries({
          upColor: color("--up"), downColor: color("--down"), borderVisible: false,
          wickUpColor: color("--up"), wickDownColor: color("--down"),
        });
        candles.setData(d.candles);
        line(chart, d.ma50, "--ma50");
        line(chart, d.ma200, "--ma200", 2);
        if (d.volume && d.volume.length) {
          candles.priceScale().applyOptions({ scaleMargins: { top: 0.05, bottom: 0.25 } });
          var vol = chart.addHistogramSeries({ priceScaleId: "vol", priceFormat: { type: "volume" }, priceLineVisible: false, lastValueVisible: false });
          chart.priceScale("vol").applyOptions({ scaleMargins: { top: 0.8, bottom: 0 } });
          var up = color("--up") + "66", down = color("--down") + "66";
          vol.setData(d.volume.map(function (v) { return { time: v.time, value: v.value, color: v.color === "down" ? down : up }; }));
        }
        if (d.markers && d.markers.length) {
          candles.setMarkers(d.markers.map(function (m) {
            return { time: m.time, position: m.position, shape: m.shape, text: m.text, color: color(m.kind === "entry" ? "--up" : "--down") };
          }));
        }
        var n = d.candles.length;
        var bars = el.clientWidth < 600 ? 70 : 130;
        var range = { from: Math.max(0, n - bars), to: n + 3 };
        chart.timeScale().setVisibleLogicalRange(range);

        var rsiEl = el.dataset.rsi && document.getElementById(el.dataset.rsi);
        if (rsiEl && d.rsi.length) {
          var rc = base(rsiEl);
          rc.applyOptions({ localization: { locale: "id-ID", priceFormatter: function (p) { return p.toFixed(0); } } });
          var rs = line(rc, d.rsi, "--accent", 2);
          rs.applyOptions({ lastValueVisible: true });
          [30, 70].forEach(function (lvl) {
            rs.createPriceLine({ price: lvl, color: color("--muted"), lineWidth: 1, lineStyle: LC.LineStyle.Dashed, axisLabelVisible: false });
          });
          rc.timeScale().setVisibleLogicalRange({ from: range.from - (n - d.rsi.length), to: range.to - (n - d.rsi.length) });
          // Scrolling one chart scrolls the other.
          var syncing = false;
          function follow(from, to, offset) {
            from.timeScale().subscribeVisibleLogicalRangeChange(function (r) {
              if (!r || syncing) return;
              syncing = true;
              to.timeScale().setVisibleLogicalRange({ from: r.from + offset, to: r.to + offset });
              syncing = false;
            });
          }
          var off = n - d.rsi.length;
          follow(chart, rc, -off);
          follow(rc, chart, off);
        }
      })
      .catch(function () { el.textContent = "Grafik gagal dimuat."; el.classList.add("chart-error"); });
  }

  document.querySelectorAll("[data-chart]").forEach(draw);
})();
