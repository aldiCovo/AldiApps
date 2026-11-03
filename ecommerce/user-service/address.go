package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Address is where an order is sent. order-service checks the same rules, because it
// receives the address with each order rather than reading it from here.
type Address struct {
	Recipient  string `json:"recipient"`
	Phone      string `json:"phone"`
	Street     string `json:"street"`
	District   string `json:"district"`
	City       string `json:"city"`
	Province   string `json:"province"`
	PostalCode string `json:"postal_code"`
}

var (
	// An Indonesian mobile number, in its 08… form after cleanAddress.
	mobilePhone = regexp.MustCompile(`^08[0-9]{7,12}$`)
	postalCode  = regexp.MustCompile(`^[0-9]{5}$`)
)

// cleanPhone removes spaces, dashes and dots, and writes +62 or 62 as 0.
func cleanPhone(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '.' {
			return -1
		}
		return r
	}, value)
	if rest, ok := strings.CutPrefix(value, "+62"); ok {
		return "0" + rest
	}
	if rest, ok := strings.CutPrefix(value, "62"); ok {
		return "0" + rest
	}
	return value
}

func checkText(label, value string, limit int) error {
	switch {
	case value == "":
		return fmt.Errorf("%s is required", label)
	case utf8.RuneCountInString(value) > limit:
		return fmt.Errorf("%s must be at most %d characters", label, limit)
	case strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) >= 0:
		return fmt.Errorf("%s must not contain control characters", label)
	}
	return nil
}

// validate trims each field and writes the phone number in one form.
func (a *Address) validate() error {
	for _, field := range []*string{&a.Recipient, &a.Phone, &a.Street, &a.District, &a.City, &a.Province, &a.PostalCode} {
		*field = strings.TrimSpace(*field)
	}
	// Only the street may have line breaks, as people write long addresses on several lines.
	for _, field := range []struct {
		label string
		value string
		limit int
	}{
		{"recipient", a.Recipient, 100},
		{"street", a.Street, 300},
		{"district", a.District, 100},
		{"city", a.City, 100},
		{"province", a.Province, 100},
	} {
		if err := checkText(field.label, field.value, field.limit); err != nil {
			return err
		}
		if field.label != "street" && strings.ContainsRune(field.value, '\n') {
			return fmt.Errorf("%s must be one line", field.label)
		}
	}
	province, ok := canonicalProvince(a.Province)
	if !ok {
		return errors.New("province must be one of Indonesia's provinces, such as Jawa Barat")
	}
	a.Province = province
	a.Phone = cleanPhone(a.Phone)
	if !mobilePhone.MatchString(a.Phone) {
		return errors.New("phone must be an Indonesian mobile number, such as 0812 3456 7890")
	}
	if !postalCode.MatchString(a.PostalCode) {
		return errors.New("postal_code must be 5 digits")
	}
	return nil
}

// provinces are Indonesia's 38 provinces. An address must name one of them, so shipping can
// be priced. user-service and order-service keep the same list.
var provinces = []string{
	"Aceh", "Sumatera Utara", "Sumatera Barat", "Riau", "Kepulauan Riau", "Jambi",
	"Sumatera Selatan", "Kepulauan Bangka Belitung", "Bengkulu", "Lampung",
	"Banten", "DKI Jakarta", "Jawa Barat", "Jawa Tengah", "DI Yogyakarta", "Jawa Timur",
	"Bali", "Nusa Tenggara Barat", "Nusa Tenggara Timur",
	"Kalimantan Barat", "Kalimantan Tengah", "Kalimantan Selatan", "Kalimantan Timur", "Kalimantan Utara",
	"Sulawesi Utara", "Gorontalo", "Sulawesi Tengah", "Sulawesi Barat", "Sulawesi Selatan", "Sulawesi Tenggara",
	"Maluku", "Maluku Utara",
	"Papua", "Papua Barat", "Papua Barat Daya", "Papua Tengah", "Papua Pegunungan", "Papua Selatan",
}

// canonicalProvince returns the list's spelling of a province, ignoring case.
func canonicalProvince(value string) (string, bool) {
	for _, province := range provinces {
		if strings.EqualFold(province, value) {
			return province, true
		}
	}
	return "", false
}
