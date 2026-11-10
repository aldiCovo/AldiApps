package main

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The broker pages: paste a broker summary from Stockbit, see what the brokers did, and keep the
// history per stock.

// maxPaste is far above a full broker table, a few kilobytes.
const maxPaste = 256 << 10

var (
	investorNames = map[string]string{"all": "Semua investor", "foreign": "Asing", "domestic": "Domestik"}
	marketNames   = map[string]string{"regular": "Reguler", "negotiated": "Nego", "cash": "Tunai"}
	modeNames     = map[string]string{"net": "Net", "gross": "Gross"}
)

// sameOrigin refuses a form posted from another site. The session cookie is SameSite=Lax, which
// already keeps it off such posts; this is a second lock.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					http.Error(w, "form dari situs lain ditolak", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

type brokerItem struct {
	brokerSummary
	Analysis brokerAnalysis
}

type brokerFormData struct {
	Error string
	// Existing is the summary a paste would replace, with New the paste, shown for a yes or no.
	Existing *brokerSummary
	New      *brokerSummary
	Text     string
	Investor string
	Market   string
	Mode     string
	Recent   []brokerItem
	Foreign  []string
}

func (u *webUI) brokerItems(r *http.Request, ticker string, limit int) ([]brokerItem, map[string]bool, error) {
	foreign, err := u.store.ForeignBrokers(r.Context())
	if err != nil {
		return nil, nil, err
	}
	list, err := u.store.BrokerSummaries(r.Context(), ticker, limit)
	if err != nil {
		return nil, nil, err
	}
	var items []brokerItem
	for _, b := range list {
		items = append(items, brokerItem{b, analyzeBrokers(b, foreign)})
	}
	return items, foreign, nil
}

func (u *webUI) brokerForm(w http.ResponseWriter, r *http.Request) {
	u.renderBrokerForm(w, r, http.StatusOK, brokerFormData{Investor: "all", Market: "regular", Mode: "net"})
}

func (u *webUI) renderBrokerForm(w http.ResponseWriter, r *http.Request, status int, d brokerFormData) {
	items, foreign, err := u.brokerItems(r, "", 50)
	if err != nil {
		u.fail(w, err)
		return
	}
	d.Recent, d.Foreign = items, sortedCodes(foreign)
	u.render(w, r, status, "broker", page{Title: "Broker", Active: "broker", View: u.job.view.Load(), Data: d})
}

func (u *webUI) brokerPaste(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPaste)
	d := brokerFormData{
		Text:     r.PostFormValue("text"),
		Investor: pick(r.PostFormValue("investor"), investorNames, "all"),
		Market:   pick(r.PostFormValue("market"), marketNames, "regular"),
		Mode:     pick(r.PostFormValue("mode"), modeNames, "net"),
	}
	s, err := parseBrokerSummary(d.Text)
	if err != nil {
		d.Error = "Teks tidak terbaca: " + err.Error() + "."
		u.renderBrokerForm(w, r, http.StatusUnprocessableEntity, d)
		return
	}
	s.Investor, s.Market, s.Mode = d.Investor, d.Market, d.Mode
	// A different paste for the same stock, period and filter replaces the kept one only once
	// confirmed, and the confirmation names which one, so a changed filter is checked again.
	existing, err := u.store.FindBrokerSummary(r.Context(), s)
	switch {
	case errors.Is(err, errNotFound):
	case err != nil:
		u.fail(w, err)
		return
	case !samePaste(existing.Pasted, s.Pasted) && r.PostFormValue("replace") != strconv.FormatInt(existing.ID, 10):
		d.Existing, d.New = &existing, &s
		u.renderBrokerForm(w, r, http.StatusConflict, d)
		return
	}
	id, err := u.store.SaveBrokerSummary(r.Context(), s)
	if err != nil {
		u.fail(w, err)
		return
	}
	http.Redirect(w, r, "/broker/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func pick(v string, allowed map[string]string, fallback string) string {
	if _, ok := allowed[v]; ok {
		return v
	}
	return fallback
}

type brokerDetailData struct {
	Item    brokerItem
	History []brokerItem
	Price   float64
	HasView bool
	Foreign map[string]bool
}

func (u *webUI) brokerDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s, err := u.store.BrokerSummary(r.Context(), id)
	if errors.Is(err, errNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		u.fail(w, err)
		return
	}
	history, foreign, err := u.brokerItems(r, s.Ticker, 100)
	if err != nil {
		u.fail(w, err)
		return
	}
	d := brokerDetailData{Item: brokerItem{s, analyzeBrokers(s, foreign)}, History: history, Price: math.NaN(), Foreign: foreign}
	v := u.job.view.Load()
	if v != nil {
		if st := v.Stocks[s.Ticker]; st != nil {
			d.Price, d.HasView = st.closes[len(st.closes)-1], true
		}
	}
	u.render(w, r, http.StatusOK, "broker_detail", page{Title: s.Ticker + " broker", Active: "broker", View: v, Data: d})
}

func (u *webUI) brokerDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := u.store.DeleteBrokerSummary(r.Context(), id); err != nil {
		u.fail(w, err)
		return
	}
	http.Redirect(w, r, "/broker", http.StatusSeeOther)
}

var codeList = regexp.MustCompile(`[A-Za-z]{2}`)

func (u *webUI) foreignForm(w http.ResponseWriter, r *http.Request) {
	foreign, err := u.store.ForeignBrokers(r.Context())
	if err != nil {
		u.fail(w, err)
		return
	}
	u.render(w, r, http.StatusOK, "broker_foreign", page{Title: "Broker asing", Active: "broker",
		View: u.job.view.Load(), Data: strings.Join(sortedCodes(foreign), " ")})
}

func (u *webUI) foreignSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var codes []string
	for _, c := range codeList.FindAllString(r.PostFormValue("codes"), -1) {
		codes = append(codes, strings.ToUpper(c))
	}
	if err := u.store.SetForeignBrokers(r.Context(), codes); err != nil {
		u.fail(w, err)
		return
	}
	http.Redirect(w, r, "/broker", http.StatusSeeOther)
}
