package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

var errUserGone = errors.New("the user no longer exists")

// User is who an email goes to.
type User struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// userFinder is what the notifier needs from user-service.
type userFinder interface {
	Find(ctx context.Context, id int64) (User, error)
}

// UserClient reads each address when it's needed, so this service keeps no copy of them.
type UserClient struct {
	url, token string
	client     *http.Client
}

func newUserClient(url, token string) *UserClient {
	return &UserClient{url: url, token: token, client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *UserClient) Find(ctx context.Context, id int64) (User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/internal/users/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return User{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return User{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return User{}, errUserGone
	default:
		return User{}, fmt.Errorf("user-service answered %d", resp.StatusCode)
	}
	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return User{}, err
	}
	if user.Email == "" {
		return User{}, errors.New("user-service sent a user without an email")
	}
	return user, nil
}
