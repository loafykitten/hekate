package money

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Item is a connected bank, as the API shows it.
type Item struct {
	ID          string  `json:"id"`
	Institution string  `json:"institution"`
	Status      string  `json:"status"`
	Error       string  `json:"error"`
	LastSyncAt  *string `json:"last_sync_at"`
	Accounts    int     `json:"accounts"`
}

// Balance is an account's balances; a figure is null when the bank doesn't say.
// A credit account's current balance is what is owed.
type Balance struct {
	Current   *float64 `json:"current"`
	Available *float64 `json:"available"`
	Limit     *float64 `json:"limit"`
}

// Account is a bank account. Its names are the bank's text.
type Account struct {
	ID          string  `json:"id"`
	ItemID      string  `json:"item_id"`
	Institution string  `json:"institution"`
	Name        string  `json:"name"`
	Mask        string  `json:"mask"`
	Type        string  `json:"type"`
	Subtype     string  `json:"subtype"`
	Balance     Balance `json:"balance"`
	Currency    string  `json:"currency"`
	UpdatedAt   string  `json:"updated_at"`
}

// Transaction is money in (+) or out (-) of an account. Name and merchant are the bank's text.
type Transaction struct {
	ID            string  `json:"id"`
	AccountID     string  `json:"account_id"`
	Account       string  `json:"account"`
	Date          string  `json:"date"`
	Name          string  `json:"name"`
	Merchant      *string `json:"merchant"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	Pending       bool    `json:"pending"`
	Category      *string `json:"category"`
	CategoryLabel *string `json:"category_label"`
	Detail        *string `json:"detail"`
}

// item is a stored bank including its secrets; it never leaves the package.
type item struct {
	id          int64
	plaidID     string
	accessToken string
	institution string
	cursor      string
}

// Items lists the connected banks by name.
func (s *Service) Items(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.institution_name, i.status, i.error, i.last_sync_at,
		       (SELECT COUNT(*) FROM money_accounts a WHERE a.item_id = i.id AND a.active = 1)
		FROM money_items i ORDER BY i.institution_name COLLATE NOCASE, i.id`)
	if err != nil {
		return nil, fmt.Errorf("list banks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Item{}
	for rows.Next() {
		var it Item
		var id int64
		var last sql.NullString
		if err := rows.Scan(&id, &it.Institution, &it.Status, &it.Error, &last, &it.Accounts); err != nil {
			return nil, fmt.Errorf("read a bank: %w", err)
		}
		it.ID, it.LastSyncAt = strconv.FormatInt(id, 10), s.local(last)
		out = append(out, it)
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

func (s *Service) itemByID(ctx context.Context, id int64) (Item, error) {
	items, err := s.Items(ctx)
	if err != nil {
		return Item{}, err
	}
	want := strconv.FormatInt(id, 10)
	for _, it := range items {
		if it.ID == want {
			return it, nil
		}
	}
	return Item{}, errNoItem(want)
}

// storedItems loads every bank with its secrets, for syncing.
func (s *Service) storedItems(ctx context.Context) ([]item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, plaid_item_id, access_token, institution_name, cursor FROM money_items ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load banks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.plaidID, &it.accessToken, &it.institution, &it.cursor); err != nil {
			return nil, fmt.Errorf("read a bank: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

func (s *Service) storedItem(ctx context.Context, id int64) (item, error) {
	var it item
	err := s.db.QueryRowContext(ctx, `SELECT id, plaid_item_id, access_token, institution_name, cursor FROM money_items WHERE id = ?`, id).
		Scan(&it.id, &it.plaidID, &it.accessToken, &it.institution, &it.cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return item{}, errNoItem(strconv.FormatInt(id, 10))
	}
	if err != nil {
		return item{}, fmt.Errorf("load bank: %w", err)
	}
	return it, nil
}

// Accounts lists every open account by institution, then name.
func (s *Service) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.account_id, a.item_id, i.institution_name, a.name, a.mask, a.type, a.subtype,
		       a.current_cents, a.available_cents, a.limit_cents, a.currency, a.updated_at
		FROM money_accounts a JOIN money_items i ON i.id = a.item_id
		WHERE a.active = 1
		ORDER BY i.institution_name COLLATE NOCASE, a.name COLLATE NOCASE, a.account_id`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Account{}
	for rows.Next() {
		var a Account
		var itemID int64
		var cur, avail, limit sql.NullInt64
		var updated string
		if err := rows.Scan(&a.ID, &itemID, &a.Institution, &a.Name, &a.Mask, &a.Type, &a.Subtype, &cur, &avail, &limit, &a.Currency, &updated); err != nil {
			return nil, fmt.Errorf("read an account: %w", err)
		}
		a.ItemID = strconv.FormatInt(itemID, 10)
		a.Balance = Balance{Current: dollarsPtr(cur), Available: dollarsPtr(avail), Limit: dollarsPtr(limit)}
		if p := s.local(sql.NullString{String: updated, Valid: true}); p != nil {
			a.UpdatedAt = *p
		}
		out = append(out, a)
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

// Query filters transactions. Empty fields don't filter.
type Query struct {
	Text     string // words in the name or merchant
	Account  string // account id
	Category string // Plaid primary category, any case or spacing
	From, To string // inclusive dates, YYYY-MM-DD
	Limit    int
}

// normalizeCategory turns "Food and drink" into FOOD_AND_DRINK.
func normalizeCategory(c string) string {
	return strings.ToUpper(strings.Join(strings.FieldsFunc(c, func(r rune) bool { return r == ' ' || r == '_' || r == '-' }), "_"))
}

func validDate(v string) bool {
	_, err := time.Parse(time.DateOnly, v)
	return err == nil
}

// likeEscape makes s safe inside a LIKE pattern that uses ESCAPE '\'.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Transactions returns matching transactions, newest first and pending first within a day.
func (s *Service) Transactions(ctx context.Context, q Query) ([]Transaction, error) {
	var where []string
	var args []any
	if q.Text = strings.TrimSpace(q.Text); q.Text != "" {
		pat := "%" + likeEscape(q.Text) + "%"
		where = append(where, `(t.name LIKE ? ESCAPE '\' OR t.merchant LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	if q.Account != "" {
		where = append(where, `t.account_id = ?`)
		args = append(args, q.Account)
	}
	if q.Category != "" {
		where = append(where, `t.category = ?`)
		args = append(args, normalizeCategory(q.Category))
	}
	if q.From != "" {
		if !validDate(q.From) {
			return nil, httpx.BadRequest("from must be a date like 2026-10-01")
		}
		where = append(where, `t.date >= ?`)
		args = append(args, q.From)
	}
	if q.To != "" {
		if !validDate(q.To) {
			return nil, httpx.BadRequest("to must be a date like 2026-10-31")
		}
		where = append(where, `t.date <= ?`)
		args = append(args, q.To)
	}
	if q.Limit <= 0 {
		q.Limit = defaultList
	}
	query := `
		SELECT t.transaction_id, t.account_id, COALESCE(a.name, ''), t.date, t.name, t.merchant,
		       t.amount_cents, t.currency, t.pending, t.category, t.detail
		FROM money_transactions t LEFT JOIN money_accounts a ON a.account_id = t.account_id`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `) //nolint:gosec // the clauses are fixed text; every value is bound
	}
	query += ` ORDER BY t.date DESC, t.pending DESC, t.transaction_id LIMIT ?`
	args = append(args, min(q.Limit, maxList))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Transaction{}
	for rows.Next() {
		var t Transaction
		var merchant, category, detail string
		var amount int64
		if err := rows.Scan(&t.ID, &t.AccountID, &t.Account, &t.Date, &t.Name, &merchant, &amount, &t.Currency, &t.Pending, &category, &detail); err != nil {
			return nil, fmt.Errorf("read a transaction: %w", err)
		}
		t.Amount = dollars(amount)
		t.Merchant, t.Detail = nonEmpty(merchant), nonEmpty(detail)
		if category != "" {
			label := CategoryLabel(category)
			t.Category, t.CategoryLabel = &category, &label
		}
		out = append(out, t)
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

func nonEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
