package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func addressBody(label string, main bool) string {
	return fmt.Sprintf(`{"label":%q,"recipient":"Sari","phone":"0812 3456 7890","street":"Jl. Melati 5","district":"Coblong","city":"Kota Bandung","province":"jawa barat","postal_code":"40132","main":%t}`, label, main)
}

func readList(t *testing.T, status int, body []byte) addressList {
	t.Helper()
	var list addressList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("status %d, body %s: %v", status, body, err)
	}
	return list
}

func labels(list addressList) string {
	var out []string
	for _, a := range list.Addresses {
		label := a.Label
		if a.Main {
			label += "*"
		}
		out = append(out, label)
	}
	return strings.Join(out, ",")
}

func TestAddresses(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	s.register(t, "budi@example.com", "password123")
	sari := s.login(t, "sari@example.com", "password123").Token
	budi := s.login(t, "budi@example.com", "password123").Token

	status, body := request(t, http.MethodGet, s.URL+"/me/addresses", "", sari)
	if list := readList(t, status, body); status != http.StatusOK || len(list.Addresses) != 0 || list.Limit != maxAddresses {
		t.Fatalf("empty: status %d, body %s", status, body)
	}

	// The first address is the main one even when not asked.
	status, body = request(t, http.MethodPost, s.URL+"/me/addresses", addressBody(" Rumah ", false), sari)
	list := readList(t, status, body)
	if status != http.StatusCreated || list.Address == nil || !list.Address.Main || list.Address.Label != "Rumah" || list.Address.Province != "Jawa Barat" || list.Address.Phone != "081234567890" {
		t.Fatalf("first: status %d, body %s", status, body)
	}
	rumah := list.Address.ID

	status, body = request(t, http.MethodPost, s.URL+"/me/addresses", addressBody("Kantor", false), sari)
	list = readList(t, status, body)
	kantor := list.Address.ID
	if status != http.StatusCreated || labels(list) != "Rumah*,Kantor" {
		t.Fatalf("second: status %d, %s", status, labels(list))
	}

	// Asking for main moves it.
	status, body = request(t, http.MethodPost, s.URL+"/me/addresses", addressBody("Ibu", true), sari)
	list = readList(t, status, body)
	ibu := list.Address.ID
	if labels(list) != "Ibu*,Rumah,Kantor" {
		t.Fatalf("third: %s", labels(list))
	}
	// The old route reads and changes the main address.
	if _, body := request(t, http.MethodGet, s.URL+"/me/address", "", sari); !strings.Contains(string(body), `"recipient":"Sari"`) || strings.Contains(string(body), "label") {
		t.Fatalf("old route: %s", body)
	}

	status, body = request(t, http.MethodPut, fmt.Sprintf("%s/me/addresses/%d/main", s.URL, kantor), "", sari)
	if list := readList(t, status, body); status != http.StatusOK || labels(list) != "Kantor*,Rumah,Ibu" {
		t.Fatalf("set main: status %d, %s", status, labels(list))
	}

	// Changing an address keeps its place; main=false doesn't take main away.
	status, body = request(t, http.MethodPut, fmt.Sprintf("%s/me/addresses/%d", s.URL, kantor), strings.Replace(addressBody("Kantor Baru", false), "40132", "40111", 1), sari)
	list = readList(t, status, body)
	if status != http.StatusOK || list.Address.PostalCode != "40111" || labels(list) != "Kantor Baru*,Rumah,Ibu" {
		t.Fatalf("update: status %d, body %s", status, body)
	}
	status, body = request(t, http.MethodPut, fmt.Sprintf("%s/me/addresses/%d", s.URL, rumah), addressBody("Rumah", true), sari)
	if list := readList(t, status, body); labels(list) != "Rumah*,Kantor Baru,Ibu" {
		t.Fatalf("update to main: %s", labels(list))
	}

	// Deleting the main one makes the oldest left the main one.
	status, body = request(t, http.MethodDelete, fmt.Sprintf("%s/me/addresses/%d", s.URL, rumah), "", sari)
	if list := readList(t, status, body); status != http.StatusOK || labels(list) != "Kantor Baru*,Ibu" {
		t.Fatalf("delete main: status %d, %s", status, labels(list))
	}

	// Another account can't see, change, choose or delete them.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPut, fmt.Sprintf("/me/addresses/%d", ibu), addressBody("X", false)},
		{http.MethodPut, fmt.Sprintf("/me/addresses/%d/main", ibu), ""},
		{http.MethodDelete, fmt.Sprintf("/me/addresses/%d", ibu), ""},
		{http.MethodDelete, "/me/addresses/abc", ""},
		{http.MethodDelete, "/me/addresses/0", ""},
	} {
		if status, body := request(t, c.method, s.URL+c.path, c.body, budi); status != http.StatusNotFound {
			t.Errorf("budi %s %s: status %d, body %s", c.method, c.path, status, body)
		}
	}
	status, body = request(t, http.MethodGet, s.URL+"/me/addresses", "", budi)
	if list := readList(t, status, body); len(list.Addresses) != 0 {
		t.Fatalf("budi sees: %s", body)
	}
	// A failed choice leaves the main one as it was.
	request(t, http.MethodPut, s.URL+"/me/addresses/999999/main", "", sari)
	status, body = request(t, http.MethodGet, s.URL+"/me/addresses", "", sari)
	if list := readList(t, status, body); labels(list) != "Kantor Baru*,Ibu" {
		t.Fatalf("after a failed choice: %s", labels(list))
	}

	for _, bad := range []string{
		addressBody("", false),
		addressBody(strings.Repeat("a", maxLabelLength+1), false),
		addressBody("Dua\nbaris", false),
		strings.Replace(addressBody("Rumah", false), "40132", "4013", 1),
		strings.Replace(addressBody("Rumah", false), `"main":false`, `"main":false,"id":5`, 1),
	} {
		if status, body := request(t, http.MethodPost, s.URL+"/me/addresses", bad, sari); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s", bad, status, body)
		}
	}
	if status, _ := request(t, http.MethodGet, s.URL+"/me/addresses", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", status)
	}
}

func TestAddressLimit(t *testing.T) {
	s := newTestServer(t, nil)
	s.register(t, "sari@example.com", "password123")
	sari := s.login(t, "sari@example.com", "password123").Token

	// Many at once still stop at the limit, with exactly one main address.
	var wg sync.WaitGroup
	statuses := make(chan int, maxAddresses+5)
	for i := range maxAddresses + 5 {
		wg.Go(func() {
			status, _ := request(t, http.MethodPost, s.URL+"/me/addresses", addressBody(fmt.Sprintf("A%d", i), i%2 == 0), sari)
			statuses <- status
		})
	}
	wg.Wait()
	close(statuses)
	created, refused := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("status %d", status)
		}
	}
	status, body := request(t, http.MethodGet, s.URL+"/me/addresses", "", sari)
	list := readList(t, status, body)
	mains := 0
	for _, a := range list.Addresses {
		if a.Main {
			mains++
		}
	}
	if created != maxAddresses || refused != 5 || len(list.Addresses) != maxAddresses || mains != 1 {
		t.Fatalf("created %d, refused %d, stored %d, main %d", created, refused, len(list.Addresses), mains)
	}
}
