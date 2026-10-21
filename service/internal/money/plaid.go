package money

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Plaid's base URLs by environment.
const (
	productionURL = "https://production.plaid.com"
	sandboxURL    = "https://sandbox.plaid.com"
)

// maxResponse caps what is read from Plaid: a sync page of 500 transactions is far below it.
const maxResponse = 16 << 20

// Plaid error codes the service reacts to.
const (
	codeLoginRequired     = "ITEM_LOGIN_REQUIRED"
	codePendingExpiration = "PENDING_EXPIRATION"
	codePendingDisconnect = "PENDING_DISCONNECT"
	codeMutation          = "TRANSACTIONS_SYNC_MUTATION_DURING_PAGINATION"
	codeItemNotFound      = "ITEM_NOT_FOUND"
)

// Request settings.
const (
	maxSyncRestarts   = 3   // passes tried when the data keeps changing under a sync
	syncPageSize      = 500 // transactions per /transactions/sync page
	linkDaysRequested = 730 // how far back a new bank's history reaches
	linkClientName    = "Hekate"
	linkClientUserID  = "owner"
	linkLanguage      = "en"
	linkCountry       = "US"
	linkProduct       = "transactions" // the only product ever enabled: nothing here can move money
)

// Plaid endpoints.
const (
	pathLinkCreate = "/link/token/create"
	pathExchange   = "/item/public_token/exchange"
	pathSync       = "/transactions/sync"
	pathAccounts   = "/accounts/get"
	pathItemRemove = "/item/remove"
)

// plaidError is an error answer from Plaid. Its fields are Plaid's own
// diagnostics (never the request), so they are safe to log.
type plaidError struct {
	Type    string `json:"error_type"`
	Code    string `json:"error_code"`
	Message string `json:"error_message"`
	Display string `json:"display_message"`
	Status  int    `json:"-"`
}

func (e *plaidError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("plaid answered %d", e.Status)
	}
	return fmt.Sprintf("plaid: %s (%s)", e.Code, e.Type)
}

// reason is what the owner is told: Plaid's display message when it has one.
func (e *plaidError) reason() string {
	switch {
	case e.Display != "":
		return e.Display
	case e.Message != "":
		return e.Message
	}
	return e.Error()
}

// needsLogin reports whether the owner has to sign in to the bank again.
func (e *plaidError) needsLogin() bool {
	switch e.Code {
	case codeLoginRequired, codePendingExpiration, codePendingDisconnect:
		return true
	}
	return false
}

// client talks to Plaid. It never logs: requests carry the secret and access
// tokens, and responses carry transactions.
type client struct {
	http     *http.Client
	base     string
	clientID string
	secret   string
}

// call posts req (plus the credentials) to path and decodes the answer into out.
func (c *client) call(ctx context.Context, path string, req map[string]any, out any) error {
	body := make(map[string]any, len(req)+2)
	for k, v := range req {
		body[k] = v
	}
	body["client_id"], body["secret"] = c.clientID, c.secret
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", path, err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("build %s request: %w", path, err)
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(hr)
	if err != nil {
		// The url.Error text holds only the URL and cause, never the body.
		return fmt.Errorf("call plaid %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("read plaid %s answer: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		pe := &plaidError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, pe) // not JSON: just the status
		return pe
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode plaid %s answer: %w", path, err)
	}
	return nil
}

// linkToken asks for a token to open Plaid Link. With an access token it is for
// update mode (the owner signs in to that bank again), which takes no products.
func (c *client) linkToken(ctx context.Context, accessToken, redirectURI string) (string, error) {
	req := map[string]any{
		"client_name":   linkClientName,
		"user":          map[string]any{"client_user_id": linkClientUserID},
		"country_codes": []string{linkCountry},
		"language":      linkLanguage,
	}
	if accessToken != "" {
		req["access_token"] = accessToken
	} else {
		req["products"] = []string{linkProduct}
		req["transactions"] = map[string]any{"days_requested": linkDaysRequested}
	}
	if redirectURI != "" {
		req["redirect_uri"] = redirectURI
	}
	var out struct {
		LinkToken string `json:"link_token"`
	}
	if err := c.call(ctx, pathLinkCreate, req, &out); err != nil {
		return "", err
	}
	if out.LinkToken == "" {
		return "", errors.New("plaid returned no link token")
	}
	return out.LinkToken, nil
}

// exchange trades the public token Link produced for the item's access token.
func (c *client) exchange(ctx context.Context, publicToken string) (accessToken, itemID string, err error) {
	var out struct {
		AccessToken string `json:"access_token"`
		ItemID      string `json:"item_id"`
	}
	if err := c.call(ctx, pathExchange, map[string]any{"public_token": publicToken}, &out); err != nil {
		return "", "", err
	}
	if out.AccessToken == "" || out.ItemID == "" {
		return "", "", errors.New("plaid returned no access token")
	}
	return out.AccessToken, out.ItemID, nil
}

// plaidTxn is a transaction as Plaid sends it. Amounts are positive for money out.
type plaidTxn struct {
	ID             string  `json:"transaction_id"`
	AccountID      string  `json:"account_id"`
	Date           string  `json:"date"`
	AuthorizedDate string  `json:"authorized_date"`
	Name           string  `json:"name"`
	MerchantName   string  `json:"merchant_name"`
	Amount         float64 `json:"amount"`
	Currency       string  `json:"iso_currency_code"`
	Unofficial     string  `json:"unofficial_currency_code"`
	Pending        bool    `json:"pending"`
	PaymentChannel string  `json:"payment_channel"`
	Category       *struct {
		Primary  string `json:"primary"`
		Detailed string `json:"detailed"`
	} `json:"personal_finance_category"`
}

// syncResult is everything one complete /transactions/sync pass produced.
type syncResult struct {
	Added, Modified []plaidTxn
	Removed         []string
	Cursor          string
	Status          string // transactions_update_status of the last page
}

// historyOf reads Plaid's transactions_update_status: how much of a bank's
// history has arrived. Nil when Plaid doesn't say, so the stored one stays.
func historyOf(status string) *string {
	var h string
	switch status {
	case "NOT_READY":
		h = HistoryPending
	case "INITIAL_UPDATE_COMPLETE":
		h = HistoryPartial
	case "HISTORICAL_UPDATE_COMPLETE":
		h = HistoryComplete
	default:
		return nil
	}
	return &h
}

type syncPage struct {
	Added    []plaidTxn `json:"added"`
	Modified []plaidTxn `json:"modified"`
	Removed  []struct {
		ID string `json:"transaction_id"`
	} `json:"removed"`
	NextCursor   string `json:"next_cursor"`
	HasMore      bool   `json:"has_more"`
	UpdateStatus string `json:"transactions_update_status"`
}

// sync reads every page from cursor on. When the data changes under it
// (TRANSACTIONS_SYNC_MUTATION_DURING_PAGINATION) the whole pass starts over
// from the cursor it began with, so the pages never mix two states.
func (c *client) sync(ctx context.Context, accessToken, cursor string) (syncResult, error) {
	var err error
	for range maxSyncRestarts {
		var res syncResult
		res, err = c.syncPass(ctx, accessToken, cursor)
		var pe *plaidError
		if errors.As(err, &pe) && pe.Code == codeMutation {
			continue
		}
		return res, err
	}
	return syncResult{}, err
}

func (c *client) syncPass(ctx context.Context, accessToken, cursor string) (syncResult, error) {
	res := syncResult{Cursor: cursor}
	for {
		req := map[string]any{"access_token": accessToken, "count": syncPageSize}
		if res.Cursor != "" {
			req["cursor"] = res.Cursor
		}
		var page syncPage
		if err := c.call(ctx, pathSync, req, &page); err != nil {
			return syncResult{}, err
		}
		res.Added = append(res.Added, page.Added...)
		res.Modified = append(res.Modified, page.Modified...)
		for _, r := range page.Removed {
			res.Removed = append(res.Removed, r.ID)
		}
		res.Cursor, res.Status = page.NextCursor, page.UpdateStatus
		if !page.HasMore {
			return res, nil
		}
	}
}

// plaidAccount is an account with its balances. Balances are null when the bank doesn't say.
type plaidAccount struct {
	ID           string `json:"account_id"`
	Name         string `json:"name"`
	OfficialName string `json:"official_name"`
	Mask         string `json:"mask"`
	Type         string `json:"type"`
	Subtype      string `json:"subtype"`
	Balances     struct {
		Available *float64 `json:"available"`
		Current   *float64 `json:"current"`
		Limit     *float64 `json:"limit"`
		Currency  string   `json:"iso_currency_code"`
	} `json:"balances"`
}

// accounts reads the item's accounts and their balances.
func (c *client) accounts(ctx context.Context, accessToken string) ([]plaidAccount, error) {
	var out struct {
		Accounts []plaidAccount `json:"accounts"`
	}
	if err := c.call(ctx, pathAccounts, map[string]any{"access_token": accessToken}, &out); err != nil {
		return nil, err
	}
	return out.Accounts, nil
}

// removeItem revokes the access token at Plaid. An item Plaid no longer knows counts as removed.
func (c *client) removeItem(ctx context.Context, accessToken string) error {
	err := c.call(ctx, pathItemRemove, map[string]any{"access_token": accessToken}, nil)
	var pe *plaidError
	if errors.As(err, &pe) && pe.Code == codeItemNotFound {
		return nil
	}
	return err
}

// currencyOf is the transaction's ISO currency, or the unofficial one (crypto and the like), or USD.
func currencyOf(iso, unofficial string) string {
	switch {
	case iso != "":
		return strings.ToUpper(iso)
	case unofficial != "":
		return strings.ToUpper(unofficial)
	}
	return "USD"
}
