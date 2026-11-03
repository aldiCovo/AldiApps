package main

import "errors"

// Shipping is simulated: no courier is asked. Prices go up with the distance from Java,
// where the shop sends from, by zone. An order pays one price whatever it holds, because
// the catalog has no weights yet.

// zones gives each province's distance from Java: 1 is Java itself, 4 the farthest.
var zones = map[string]int{
	"Banten": 1, "DKI Jakarta": 1, "Jawa Barat": 1, "Jawa Tengah": 1, "DI Yogyakarta": 1, "Jawa Timur": 1,
	"Aceh": 2, "Sumatera Utara": 2, "Sumatera Barat": 2, "Riau": 2, "Kepulauan Riau": 2, "Jambi": 2,
	"Sumatera Selatan": 2, "Kepulauan Bangka Belitung": 2, "Bengkulu": 2, "Lampung": 2,
	"Bali": 2, "Nusa Tenggara Barat": 2,
	"Kalimantan Barat": 3, "Kalimantan Tengah": 3, "Kalimantan Selatan": 3, "Kalimantan Timur": 3, "Kalimantan Utara": 3,
	"Sulawesi Utara": 3, "Gorontalo": 3, "Sulawesi Tengah": 3, "Sulawesi Barat": 3, "Sulawesi Selatan": 3, "Sulawesi Tenggara": 3,
	"Nusa Tenggara Timur": 4, "Maluku": 4, "Maluku Utara": 4,
	"Papua": 4, "Papua Barat": 4, "Papua Barat Daya": 4, "Papua Tengah": 4, "Papua Pegunungan": 4, "Papua Selatan": 4,
}

// shippingOption is one courier's service, with a price and an estimate for each zone, 1 to 4.
type shippingOption struct {
	ID        string    `json:"id"`
	Courier   string    `json:"courier"`
	Service   string    `json:"service"`
	Costs     [4]int64  `json:"costs"`
	Estimates [4]string `json:"estimates"`
}

var shippingOptions = []shippingOption{
	{"jne-reg", "JNE", "Reguler", [4]int64{10000, 20000, 30000, 45000}, [4]string{"2–3 hari", "3–5 hari", "4–6 hari", "5–8 hari"}},
	{"jne-express", "JNE", "Express", [4]int64{20000, 38000, 55000, 80000}, [4]string{"1 hari", "1–2 hari", "2–3 hari", "2–4 hari"}},
	{"jnt-reg", "J&T", "Reguler", [4]int64{9000, 19000, 29000, 44000}, [4]string{"2–3 hari", "3–5 hari", "4–6 hari", "5–8 hari"}},
	{"jnt-express", "J&T", "Express", [4]int64{19000, 36000, 53000, 78000}, [4]string{"1 hari", "1–2 hari", "2–3 hari", "2–4 hari"}},
	{"sicepat-reg", "SiCepat", "Reguler", [4]int64{9500, 19500, 29500, 44500}, [4]string{"2–4 hari", "3–5 hari", "4–7 hari", "5–9 hari"}},
	{"sicepat-express", "SiCepat", "Express", [4]int64{18000, 35000, 52000, 77000}, [4]string{"1 hari", "1–2 hari", "2–3 hari", "2–4 hari"}},
}

var errUnknownShipping = errors.New("shipping_option is not one of the options")

// Shipping is what an order keeps of its delivery: a copy, so a later price change
// doesn't change it.
type Shipping struct {
	Option   string `json:"option"`
	Courier  string `json:"courier"`
	Service  string `json:"service"`
	Estimate string `json:"estimate"`
	Cost     int64  `json:"cost"`
}

// quote prices an option for a province from the list.
func quote(optionID, province string) (Shipping, error) {
	zone, ok := zones[province]
	if !ok {
		return Shipping{}, errors.New("there is no shipping to this province")
	}
	for _, option := range shippingOptions {
		if option.ID == optionID {
			return Shipping{Option: option.ID, Courier: option.Courier, Service: option.Service, Estimate: option.Estimates[zone-1], Cost: option.Costs[zone-1]}, nil
		}
	}
	return Shipping{}, errUnknownShipping
}

type shippingProvince struct {
	Name string `json:"name"`
	Zone int    `json:"zone"`
}

// shippingTable is everything the checkout page needs to show prices as the province
// changes. The order is priced again here when it's made.
func shippingTable() map[string]any {
	list := make([]shippingProvince, len(provinces))
	for i, name := range provinces {
		list[i] = shippingProvince{Name: name, Zone: zones[name]}
	}
	return map[string]any{"provinces": list, "options": shippingOptions}
}
