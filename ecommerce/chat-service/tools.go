package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// Shop reads what the bot may tell a customer. Orders and vouchers are read with the
// customer's own token, so the bot can only ever see that customer's.
type Shop struct {
	searchURL  string
	catalogURL string
	orderURL   string
	userURL    string
	client     *http.Client
}

func newShop(searchURL, catalogURL, orderURL, userURL string) *Shop {
	return &Shop{searchURL: searchURL, catalogURL: catalogURL, orderURL: orderURL, userURL: userURL,
		client: &http.Client{Timeout: 8 * time.Second}}
}

// errNotThere is a product or order that doesn't exist, or isn't this customer's.
var errNotThere = errors.New("not found")

func (s *Shop) get(ctx context.Context, address, bearer string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotThere
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s answered %d", address, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into)
}

// FirstName is the customer's first name, or "" when it can't be read.
func (s *Shop) FirstName(ctx context.Context, bearer string) string {
	var me struct {
		Name string `json:"name"`
	}
	if s.get(ctx, s.userURL+"/me", bearer, &me) != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(me.Name), " ")
	return first
}

// FullName is the customer's name for the admins, or "" when it can't be read.
func (s *Shop) FullName(ctx context.Context, bearer string) string {
	var me struct {
		Name string `json:"name"`
	}
	if s.get(ctx, s.userURL+"/me", bearer, &me) != nil {
		return ""
	}
	return strings.TrimSpace(me.Name)
}

type variantInfo struct {
	Size  string `json:"size"`
	Color string `json:"color"`
	Stock int32  `json:"stock"`
}

type productInfo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Rupiah, what the customer pays now.
	Price int64 `json:"price"`
	// The regular price, only while a sale is on.
	OriginalPrice *int64        `json:"original_price,omitempty"`
	Variants      []variantInfo `json:"variants"`
}

type orderInfo struct {
	ID             int64      `json:"id"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason,omitempty"`
	Total          int64      `json:"total"`
	Discount       int64      `json:"discount,omitempty"`
	VoucherCode    string     `json:"voucher_code,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	PaidAt         *time.Time `json:"paid_at,omitempty"`
	PackedAt       *time.Time `json:"packed_at,omitempty"`
	ShippedAt      *time.Time `json:"shipped_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	TrackingNumber string     `json:"tracking_number,omitempty"`
	Shipping       *struct {
		Courier  string `json:"courier"`
		Service  string `json:"service"`
		Estimate string `json:"estimate"`
		Cost     int64  `json:"cost"`
	} `json:"shipping,omitempty"`
	ShippingAddress *struct {
		City     string `json:"city"`
		Province string `json:"province"`
	} `json:"shipping_address,omitempty"`
	Items []struct {
		ProductID   int64  `json:"product_id"`
		ProductName string `json:"product_name"`
		Size        string `json:"size"`
		Color       string `json:"color"`
		Price       int64  `json:"price"`
		Quantity    int32  `json:"quantity"`
	} `json:"items"`
}

const maxDescription = 600

func shorten(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "…"
}

// tool is one thing the bot can do, with what Claude is told about it.
type tool struct {
	name        string
	description string
	properties  map[string]any
	required    []string
}

var botTools = []tool{
	{"search_products", "Cari produk toko dengan kata kunci (nama, jenis, warna, bahan). Mengembalikan paling banyak 5 produk dengan harga dan stok per ukuran/warna.",
		map[string]any{"query": map[string]any{"type": "string", "description": "Kata kunci, misalnya \"dress batik\" atau \"hijab hitam\""}}, []string{"query"}},
	{"get_product", "Detail satu produk: deskripsi, harga sekarang, harga coret saat diskon, dan stok tiap ukuran/warna.",
		map[string]any{"product_id": map[string]any{"type": "integer"}}, []string{"product_id"}},
	{"my_orders", "Lima pesanan terbaru pelanggan ini, terbaru dulu, dengan status dan total.",
		map[string]any{}, nil},
	{"get_order", "Detail satu pesanan pelanggan ini: status, waktu bayar/kemas/kirim/selesai, kurir, nomor resi, barang.",
		map[string]any{"order_id": map[string]any{"type": "integer"}}, []string{"order_id"}},
	{"shipping_options", "Daftar kurir, ongkir, dan perkiraan lama kirim. Ongkir tergantung zona provinsi tujuan (1 = Jawa, 4 = terjauh); tabel provinsi ke zona ikut dikembalikan.",
		map[string]any{}, nil},
	{"my_vouchers", "Voucher yang bisa dilihat pelanggan ini, dengan syarat, batas diskon, dan apakah masih bisa dipakai.",
		map[string]any{}, nil},
	{"attach_product", "Tampilkan kartu produk di chat agar pelanggan bisa langsung membukanya. Pakai saat menyarankan produk tertentu. Paling banyak 3 kartu per jawaban.",
		map[string]any{"product_id": map[string]any{"type": "integer"}}, []string{"product_id"}},
	{"attach_order", "Tampilkan kartu pesanan pelanggan ini di chat.",
		map[string]any{"order_id": map[string]any{"type": "integer"}}, []string{"order_id"}},
	{"handoff_to_admin", "Teruskan chat ke admin manusia. Pakai saat pelanggan minta admin, saat permintaan butuh admin (ganti alamat, batal, retur, refund, komplain, pembayaran bermasalah, pesanan khusus), atau saat kamu tidak yakin jawabannya. Admin akan dikabari. Tetap tulis jawaban singkat ke pelanggan bahwa chat sudah diteruskan.",
		map[string]any{"reason": map[string]any{"type": "string", "description": "Alasan singkat untuk admin, misalnya \"ganti alamat pesanan #41\""}}, []string{"reason"}},
}

func toolParams() []anthropic.ToolUnionParam {
	params := make([]anthropic.ToolUnionParam, len(botTools))
	for i, t := range botTools {
		params[i] = anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        t.name,
			Description: anthropic.String(t.description),
			InputSchema: anthropic.ToolInputSchemaParam{Properties: t.properties, Required: t.required},
		}}
	}
	return params
}

const maxCardsPerAnswer = 3

// turn is what one answer collects from the tools besides text.
type turn struct {
	cards   []*Attachment
	handoff string
}

// run does one tool call and returns what Claude reads back, and whether it failed.
func (s *Shop) run(ctx context.Context, cards attacher, bearer, name string, raw json.RawMessage, t *turn) (string, bool) {
	var in struct {
		Query     string `json:"query"`
		ProductID int64  `json:"product_id"`
		OrderID   int64  `json:"order_id"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "input tidak valid: " + err.Error(), true
	}
	var result any
	var err error
	switch name {
	case "search_products":
		var found struct {
			Total    int           `json:"total"`
			Products []productInfo `json:"products"`
		}
		err = s.get(ctx, s.searchURL+"/search?limit=5&q="+url.QueryEscape(shorten(in.Query, 100)), "", &found)
		for i := range found.Products {
			found.Products[i].Description = shorten(found.Products[i].Description, 200)
		}
		result = found
	case "get_product":
		var product productInfo
		err = s.get(ctx, s.catalogURL+"/products/"+strconv.FormatInt(in.ProductID, 10), "", &product)
		product.Description = shorten(product.Description, maxDescription)
		result = product
	case "my_orders":
		var list struct {
			Orders []orderInfo `json:"orders"`
		}
		err = s.get(ctx, s.orderURL+"/orders?limit=5", bearer, &list)
		for i := range list.Orders {
			list.Orders[i].ShippingAddress = nil
		}
		result = list
	case "get_order":
		var order orderInfo
		err = s.get(ctx, s.orderURL+"/orders/"+strconv.FormatInt(in.OrderID, 10), bearer, &order)
		result = order
	case "shipping_options":
		var table json.RawMessage
		err = s.get(ctx, s.orderURL+"/shipping", "", &table)
		result = table
	case "my_vouchers":
		var list json.RawMessage
		err = s.get(ctx, s.orderURL+"/vouchers/mine", bearer, &list)
		result = list
	case "attach_product", "attach_order":
		if len(t.cards) >= maxCardsPerAnswer {
			return "sudah 3 kartu di jawaban ini", true
		}
		var card *Attachment
		if name == "attach_product" {
			card, err = cards.Product(ctx, in.ProductID)
		} else {
			card, err = cards.Order(ctx, in.OrderID, bearer)
		}
		if errors.Is(err, errCannotAttach) {
			return "tidak ditemukan", true
		}
		if err != nil {
			return "gagal: layanan toko tidak menjawab", true
		}
		t.cards = append(t.cards, card)
		return "kartu akan tampil di atas jawabanmu", false
	case "handoff_to_admin":
		reason := strings.TrimSpace(in.Reason)
		if reason == "" {
			reason = "bot tidak bisa menjawab"
		}
		t.handoff = shorten(reason, 120)
		return "chat diteruskan; admin dikabari", false
	default:
		return "alat tidak dikenal", true
	}
	if errors.Is(err, errNotThere) {
		return "tidak ditemukan", true
	}
	if err != nil {
		return "gagal: layanan toko tidak menjawab", true
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "gagal", true
	}
	return string(data), false
}
