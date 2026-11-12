package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The chat's menu buttons. Each one posts what the customer asked, as their own message, and
// the bot's answer from the shop's data. No AI is asked, so the menu costs nothing and works
// when the AI's free requests are used up.
const (
	menuOrders   = "orders"
	menuStock    = "stock"
	menuShipping = "shipping"
	menuVouchers = "vouchers"
	menuReturns  = "returns"
	menuAdmin    = "admin"

	// Reasons the customer gives with menuAdmin, from the buttons under their orders.
	adminChangeOrder = "change_order"
	adminNotArrived  = "not_arrived"

	menuOrdersShown   = 3
	menuShippingShown = 3
	menuVariantsShown = 20
	menuVouchersShown = 5
)

type menuInput struct {
	Action    string `json:"action"`
	ProductID int64  `json:"product_id"`
	Province  string `json:"province"`
	Reason    string `json:"reason"`
}

// menuAnswer is what a menu button posts: the customer's question, then the bot's messages.
type menuAnswer struct {
	question string
	card     *Attachment
	replies  []menuReply
	handoff  string
}

type menuReply struct {
	body string
	card *Attachment
}

// errNeedProvince is the shipping button for a customer without a saved address: the page asks
// for the province and presses it again with one.
var errNeedProvince = errors.New("the customer has no address; send a province")

func addMenuRoutes(mux *http.ServeMux, store *Store, bot *Assistant,
	authenticated func(func(http.ResponseWriter, *http.Request, int64)) http.HandlerFunc) {

	mux.HandleFunc("POST /chat/menu", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		var in menuInput
		if !readJSON(w, r, &in) {
			return
		}
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		settings, err := store.BotSettings(r.Context())
		if err != nil {
			writeFailure(w, err)
			return
		}
		state, err := store.BotState(r.Context(), userID, midnight(bot.now()))
		if err != nil && !errors.Is(err, errNotFound) {
			writeFailure(w, err)
			return
		}
		if !bot.answersNow(settings) || state.BotOff {
			writeError(w, http.StatusConflict, "the menu is off now; write to the admins")
			return
		}
		answer, err := bot.menu(r.Context(), settings, in, bearer)
		switch {
		case errors.Is(err, errNeedProvince):
			writeJSON(w, http.StatusOK, map[string]string{"need": "province"})
			return
		case errors.Is(err, errBadMenu):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, errNotThere):
			writeError(w, http.StatusNotFound, "that product is not in the shop")
			return
		case err != nil:
			slog.Error("answering a menu button failed", "user", userID, "action", in.Action, "error", err)
			writeError(w, http.StatusBadGateway, "the shop's data could not be read")
			return
		}
		message, err := store.Send(r.Context(), userID, 0, answer.question, answer.card)
		switch {
		case errors.Is(err, errTooFast):
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		case err != nil:
			writeFailure(w, err)
			return
		}
		bot.signals.Message(userID, message.ID)
		for _, reply := range answer.replies {
			bot.say(r.Context(), userID, reply.body, reply.card)
		}
		if answer.handoff != "" {
			bot.handoff(r.Context(), userID, bearer, answer.handoff)
		}
		if err := store.CountBot(r.Context(), bot.now().In(jakarta), "menu"); err != nil {
			slog.Error("counting a menu use failed", "error", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

var errBadMenu = errors.New(`action must be "orders", "stock", "shipping", "vouchers", "returns" or "admin"`)

// menu reads what a button answers. It writes nothing, so a failed read posts nothing.
func (a *Assistant) menu(ctx context.Context, settings BotSettings, in menuInput, bearer string) (menuAnswer, error) {
	switch in.Action {
	case menuOrders:
		return a.menuOrders(ctx, bearer)
	case menuStock:
		if in.ProductID <= 0 {
			return menuAnswer{}, fmt.Errorf("%w: stock needs product_id", errBadMenu)
		}
		return a.menuStock(ctx, in.ProductID)
	case menuShipping:
		return a.menuShipping(ctx, strings.TrimSpace(in.Province), bearer)
	case menuVouchers:
		return a.menuVouchers(ctx, bearer)
	case menuReturns:
		answer := menuAnswer{question: "Cara retur & tukar"}
		if info := strings.TrimSpace(settings.StoreInfo); info != "" {
			answer.replies = []menuReply{{body: cleanBotText(info)}}
		} else {
			answer.replies = []menuReply{{body: botForwarded}}
			answer.handoff = "Tanya retur & tukar"
		}
		return answer, nil
	case menuAdmin:
		answer := menuAnswer{question: "Bicara dengan admin", handoff: reasonCustomer}
		switch in.Reason {
		case "":
		case adminChangeOrder:
			answer.question, answer.handoff = "Ubah alamat / batal pesanan", "Ubah alamat / batal pesanan"
		case adminNotArrived:
			answer.question, answer.handoff = "Pesanan belum sampai", "Pesanan belum sampai"
		default:
			return menuAnswer{}, fmt.Errorf(`%w: reason must be "change_order" or "not_arrived"`, errBadMenu)
		}
		answer.replies = []menuReply{{body: botAskedAdmin}}
		return answer, nil
	}
	return menuAnswer{}, errBadMenu
}

func (a *Assistant) menuOrders(ctx context.Context, bearer string) (menuAnswer, error) {
	answer := menuAnswer{question: "Status pesanan saya"}
	var list struct {
		Orders []orderInfo `json:"orders"`
	}
	if err := a.shop.get(ctx, a.shop.orderURL+"/orders?limit="+strconv.Itoa(menuOrdersShown), bearer, &list); err != nil {
		return answer, err
	}
	if len(list.Orders) == 0 {
		answer.replies = []menuReply{{body: "Kakak belum punya pesanan. Kalau sudah belanja, statusnya bisa dicek di sini ya 😊"}}
		return answer, nil
	}
	lines := []string{fmt.Sprintf("Ini %d pesanan terbaru Kakak. Ketuk kartunya untuk melihat detail dan riwayatnya.", len(list.Orders))}
	for _, o := range list.Orders {
		if o.TrackingNumber == "" {
			continue
		}
		courier := ""
		if o.Shipping != nil {
			courier = " (" + o.Shipping.Courier + " " + o.Shipping.Service + ")"
		}
		lines = append(lines, fmt.Sprintf("Resi pesanan #%d%s: %s", o.ID, courier, o.TrackingNumber))
	}
	answer.replies = append(answer.replies, menuReply{body: strings.Join(lines, "\n")})
	for _, o := range list.Orders {
		card := &Attachment{Kind: attachOrder, ID: o.ID, Status: o.Status, Total: o.Total}
		if len(o.Items) > 0 {
			card.Name = o.Items[0].ProductName
			card.More = len(o.Items) - 1
		}
		answer.replies = append(answer.replies, menuReply{card: card})
	}
	return answer, nil
}

func (a *Assistant) menuStock(ctx context.Context, productID int64) (menuAnswer, error) {
	answer := menuAnswer{question: "Cek stok & ukuran"}
	card, err := a.cards.Product(ctx, productID)
	if errors.Is(err, errCannotAttach) {
		return answer, errNotThere
	}
	if err != nil {
		return answer, err
	}
	answer.card = card
	var product productInfo
	if err := a.shop.get(ctx, a.shop.catalogURL+"/products/"+strconv.FormatInt(productID, 10), "", &product); err != nil {
		return answer, err
	}
	price := rupiah(product.Price)
	if product.OriginalPrice != nil && *product.OriginalPrice > product.Price {
		price += " (dari " + rupiah(*product.OriginalPrice) + ")"
	}
	lines := []string{"Stok " + product.Name + " · " + price + ":"}
	inStock := 0
	for i, v := range product.Variants {
		if i == menuVariantsShown {
			lines = append(lines, fmt.Sprintf("…dan %d pilihan lain di halaman produk.", len(product.Variants)-i))
			break
		}
		name := strings.Join(nonEmpty(v.Size, v.Color), " · ")
		if name == "" {
			name = "Stok"
		}
		if v.Stock > 0 {
			inStock++
			lines = append(lines, fmt.Sprintf("%s: %d", name, v.Stock))
		} else {
			lines = append(lines, name+": habis")
		}
	}
	if inStock == 0 {
		lines = append(lines, "Saat ini semua ukuran habis, Kak. Tanyakan ke admin kalau mau tahu kapan ada lagi.")
	}
	answer.replies = []menuReply{{body: strings.Join(lines, "\n")}}
	return answer, nil
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (a *Assistant) menuShipping(ctx context.Context, province, bearer string) (menuAnswer, error) {
	answer := menuAnswer{question: "Ongkir ke kotaku"}
	fromAddress := province == ""
	if fromAddress {
		var address struct {
			Province string `json:"province"`
		}
		err := a.shop.get(ctx, a.shop.userURL+"/me/address", bearer, &address)
		if errors.Is(err, errNotThere) || (err == nil && address.Province == "") {
			return answer, errNeedProvince
		}
		if err != nil {
			return answer, err
		}
		province = address.Province
	} else {
		answer.question = "Ongkir ke " + province
	}
	var table struct {
		Provinces []struct {
			Name string `json:"name"`
			Zone int    `json:"zone"`
		} `json:"provinces"`
		Options []struct {
			Courier   string    `json:"courier"`
			Service   string    `json:"service"`
			Costs     [4]int64  `json:"costs"`
			Estimates [4]string `json:"estimates"`
		} `json:"options"`
	}
	if err := a.shop.get(ctx, a.shop.orderURL+"/shipping", "", &table); err != nil {
		return answer, err
	}
	zone := 0
	for _, p := range table.Provinces {
		if p.Name == province {
			zone = p.Zone
		}
	}
	if zone < 1 || zone > 4 {
		if fromAddress {
			return answer, errNeedProvince
		}
		return answer, fmt.Errorf("%w: province is not one the shop ships to", errBadMenu)
	}
	options := table.Options
	sort.SliceStable(options, func(i, j int) bool { return options[i].Costs[zone-1] < options[j].Costs[zone-1] })
	where := province
	if fromAddress {
		where += " (alamat utama Kakak)"
	}
	lines := []string{"Ongkir ke " + where + ", yang termurah:"}
	for i, o := range options {
		if i == menuShippingShown {
			break
		}
		lines = append(lines, fmt.Sprintf("%s %s · %s · %s", o.Courier, o.Service, o.Estimates[zone-1], rupiah(o.Costs[zone-1])))
	}
	lines = append(lines, "Satu ongkir untuk satu pesanan, berapa pun barangnya.")
	answer.replies = []menuReply{{body: strings.Join(lines, "\n")}}
	return answer, nil
}

func (a *Assistant) menuVouchers(ctx context.Context, bearer string) (menuAnswer, error) {
	answer := menuAnswer{question: "Voucher saya"}
	var list struct {
		Vouchers []struct {
			Code        string `json:"code"`
			Description string `json:"description"`
			Kind        string `json:"kind"`
			Value       int64  `json:"value"`
			MaxDiscount *int64 `json:"max_discount"`
			MinSubtotal int64  `json:"min_subtotal"`
			State       string `json:"state"`
			EndsAt      string `json:"ends_at"`
		} `json:"vouchers"`
	}
	if err := a.shop.get(ctx, a.shop.orderURL+"/vouchers/mine", bearer, &list); err != nil {
		return answer, err
	}
	var lines []string
	for _, v := range list.Vouchers {
		if v.State != "usable" {
			continue
		}
		if len(lines) == menuVouchersShown {
			break
		}
		off := rupiah(v.Value)
		if v.Kind == "percent" {
			off = fmt.Sprintf("%d%%", v.Value)
			if v.MaxDiscount != nil {
				off += " (maks. " + rupiah(*v.MaxDiscount) + ")"
			}
		}
		line := v.Code + ": potongan " + off
		if v.MinSubtotal > 0 {
			line += ", min. belanja " + rupiah(v.MinSubtotal)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		answer.replies = []menuReply{{body: "Saat ini belum ada voucher yang bisa Kakak pakai. Pantau terus ya, voucher baru muncul di halaman Akun 😊"}}
		return answer, nil
	}
	lines = append([]string{"Voucher yang bisa Kakak pakai sekarang:"}, lines...)
	lines = append(lines, "Masukkan kodenya di halaman checkout.")
	answer.replies = []menuReply{{body: strings.Join(lines, "\n")}}
	return answer, nil
}
