package money

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/jobs"
)

// Job is the hourly sync.
func (s *Service) Job() jobs.Job {
	return jobs.Job{Name: JobName, Next: jobs.Every(syncEvery), Run: s.syncJob, Timeout: syncTimeout}
}

func (s *Service) syncJob(ctx context.Context) (string, error) {
	if !s.Configured() {
		return "", jobs.ErrSkip
	}
	res, err := s.SyncAll(ctx)
	if err != nil {
		return "", err
	}
	if res.Banks == 0 {
		return "", jobs.ErrSkip
	}
	return res.summary(), nil
}

// SyncResult is what one pass over every bank did.
type SyncResult struct {
	Banks  int // banks tried
	Failed int // of those, how many could not be synced
	Added  int // new transactions
}

func (r SyncResult) summary() string {
	ok := r.Banks - r.Failed
	out := fmt.Sprintf("Synced %s: ", plural(ok, "bank", "banks"))
	if r.Added == 0 {
		out += "no new transactions"
	} else {
		out += plural(r.Added, "new transaction", "new transactions")
	}
	if r.Failed > 0 {
		out += fmt.Sprintf("; %d need attention", r.Failed)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// SyncAll syncs every bank, one bank after another and never two passes at
// once. A bank that fails is marked and the rest carry on; it is an error only
// when every bank failed.
func (s *Service) SyncAll(ctx context.Context) (SyncResult, error) {
	var res SyncResult
	if !s.Configured() {
		return res, ErrNotConfigured
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	items, err := s.storedItems(ctx)
	if err != nil {
		return res, err
	}
	var failures []error
	for _, it := range items {
		res.Banks++
		added, err := s.syncItem(ctx, it)
		if err != nil {
			res.Failed++
			failures = append(failures, err)
			s.log.Warn("bank sync failed", "item", it.id, "error", err)
			continue
		}
		res.Added += added
		s.log.Info("bank synced", "item", it.id, "new", added)
	}
	if len(items) > 0 {
		s.changed()
	}
	if res.Failed > 0 && res.Failed == res.Banks {
		return res, fmt.Errorf("no bank could be synced: %w", errors.Join(failures...))
	}
	return res, nil
}

// syncSoon starts a sync in the background and returns at once.
func (s *Service) syncSoon() {
	s.wg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
		defer cancel()
		if _, err := s.SyncAll(ctx); err != nil {
			s.log.Warn("background bank sync failed", "error", err)
		}
	})
}

// syncItem brings one bank up to date: transactions first, then balances. It
// records the outcome on the item, and returns the number of new transactions.
func (s *Service) syncItem(ctx context.Context, it item) (int, error) {
	res, err := s.plaid.sync(ctx, it.accessToken, it.cursor)
	if err != nil {
		s.markFailed(ctx, it.id, err, false)
		return 0, err
	}
	added, err := s.apply(ctx, it.id, res)
	if err != nil {
		s.markFailed(ctx, it.id, err, true)
		return 0, err
	}
	accounts, err := s.plaid.accounts(ctx, it.accessToken)
	if err != nil {
		s.markFailed(ctx, it.id, err, false)
		return added, err
	}
	if err := s.saveAccounts(ctx, it.id, accounts); err != nil {
		s.markFailed(ctx, it.id, err, true)
		return added, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE money_items SET status = ?, error = '', last_sync_at = ? WHERE id = ?`,
		StatusOK, stamp(s.now()), it.id)
	if err != nil {
		return added, fmt.Errorf("record sync: %w", err)
	}
	return added, nil
}

// apply stores one finished pass and its cursor in a single transaction, so a
// pass that stops halfway leaves nothing behind. It returns how many of the
// added transactions were new here.
func (s *Service) apply(ctx context.Context, itemID int64, res syncResult) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin sync: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range res.Removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM money_transactions WHERE transaction_id = ? AND item_id = ?`, id, itemID); err != nil {
			return 0, fmt.Errorf("remove a transaction: %w", err)
		}
	}
	upsert, err := tx.PrepareContext(ctx, `
		INSERT INTO money_transactions (transaction_id, item_id, account_id, date, authorized_date, name, merchant,
			amount_cents, currency, pending, category, detail, channel)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(transaction_id) DO UPDATE SET
			account_id = excluded.account_id, date = excluded.date, authorized_date = excluded.authorized_date,
			name = excluded.name, merchant = excluded.merchant, amount_cents = excluded.amount_cents,
			currency = excluded.currency, pending = excluded.pending, category = excluded.category,
			detail = excluded.detail, channel = excluded.channel
		WHERE item_id = excluded.item_id`)
	if err != nil {
		return 0, fmt.Errorf("prepare transactions: %w", err)
	}
	defer func() { _ = upsert.Close() }()
	store := func(t plaidTxn) error {
		var category, detail string
		if t.Category != nil {
			category, detail = t.Category.Primary, t.Category.Detailed
		}
		merchant := t.MerchantName
		_, err := upsert.ExecContext(ctx, t.ID, itemID, t.AccountID, t.Date, t.AuthorizedDate, t.Name, merchant,
			cents(t.Amount), currencyOf(t.Currency, t.Unofficial), t.Pending, category, detail, t.PaymentChannel)
		return err //nolint:wrapcheck // the caller wraps
	}
	fresh := 0
	for _, t := range res.Added {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM money_transactions WHERE transaction_id = ?`, t.ID).Scan(&one)
		if err != nil && !isNoRows(err) {
			return 0, fmt.Errorf("check a transaction: %w", err)
		}
		if isNoRows(err) {
			fresh++
		}
		if err := store(t); err != nil {
			return 0, fmt.Errorf("store a transaction: %w", err)
		}
	}
	for _, t := range res.Modified {
		if err := store(t); err != nil {
			return 0, fmt.Errorf("store a transaction: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE money_items SET cursor = ?, history = COALESCE(?, history) WHERE id = ?`,
		res.Cursor, historyOf(res.Status), itemID); err != nil {
		return 0, fmt.Errorf("save the sync cursor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit sync: %w", err)
	}
	return fresh, nil
}

// saveAccounts upserts the accounts and their balances. Accounts the bank no
// longer lists (closed) are retired: their balances aren't shown any more, but
// the rows stay so old transactions keep their names.
func (s *Service) saveAccounts(ctx context.Context, itemID int64, accounts []plaidAccount) error {
	now := stamp(s.now())
	listed := make([]any, 0, len(accounts)+1)
	listed = append(listed, itemID)
	for _, a := range accounts {
		listed = append(listed, a.ID)
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO money_accounts (account_id, item_id, name, official_name, mask, type, subtype,
				current_cents, available_cents, limit_cents, currency, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_id) DO UPDATE SET
				name = excluded.name, official_name = excluded.official_name, mask = excluded.mask,
				type = excluded.type, subtype = excluded.subtype, current_cents = excluded.current_cents,
				available_cents = excluded.available_cents, limit_cents = excluded.limit_cents,
				currency = excluded.currency, active = 1, updated_at = excluded.updated_at
			WHERE item_id = excluded.item_id`,
			a.ID, itemID, a.Name, a.OfficialName, a.Mask, a.Type, a.Subtype,
			balanceCents(a.Balances.Current), balanceCents(a.Balances.Available), balanceCents(a.Balances.Limit),
			currencyOf(a.Balances.Currency, ""), now)
		if err != nil {
			return fmt.Errorf("store an account: %w", err)
		}
	}
	if len(accounts) == 0 {
		return nil // an empty answer retires nothing: more likely a hiccup than every account closed
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(accounts)), ",")
	if _, err := s.db.ExecContext(ctx, `UPDATE money_accounts SET active = 0 WHERE item_id = ? AND account_id NOT IN (`+marks+`)`, listed...); err != nil { //nolint:gosec // only placeholders are joined in
		return fmt.Errorf("retire closed accounts: %w", err)
	}
	return nil
}

// duplicate reports whether a bank just linked is one already connected: the
// same institution with an open account of the same kind and last digits (or
// name, when the bank gives no digits). Linking it again would count every
// transaction twice.
func (s *Service) duplicate(ctx context.Context, institutionID, plaidID string, accounts []plaidAccount) (bool, error) {
	if institutionID == "" || len(accounts) == 0 {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.mask, a.subtype, a.name FROM money_accounts a JOIN money_items i ON i.id = a.item_id
		WHERE i.institution_id = ? AND i.plaid_item_id <> ? AND a.active = 1`, institutionID, plaidID)
	if err != nil {
		return false, fmt.Errorf("check for the bank: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var mask, subtype, name string
		if err := rows.Scan(&mask, &subtype, &name); err != nil {
			return false, fmt.Errorf("check for the bank: %w", err)
		}
		for _, a := range accounts {
			if a.Subtype == subtype && ((mask != "" && a.Mask == mask) || (mask == "" && a.Mask == "" && a.Name == name)) {
				return true, nil
			}
		}
	}
	return false, rows.Err() //nolint:wrapcheck // plain iteration error
}

// markFailed records why a bank could not be synced: a bank that wants the
// owner to sign in again is login_required, anything else is an error. The
// stored reason is Plaid's own message (or a fixed sentence), never the request.
func (s *Service) markFailed(ctx context.Context, id int64, cause error, storing bool) {
	if ctx.Err() != nil {
		return // shutting down or timed out: not the bank's fault
	}
	status, reason := StatusError, "Couldn't reach the bank data service."
	var pe *plaidError
	switch {
	case errors.As(cause, &pe):
		reason = pe.reason()
		if pe.needsLogin() {
			status = StatusLoginRequired
		}
	case storing:
		reason = "Couldn't store the bank's data."
	}
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE money_items SET status = ?, error = ? WHERE id = ?`, status, reason, id); err != nil {
		s.log.Error("cannot record a bank's status", "item", id, "error", err)
	}
}

// LinkToken starts Plaid Link: for a new bank, or with itemID to reconnect that one.
func (s *Service) LinkToken(ctx context.Context, itemID string) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	access := ""
	if itemID != "" {
		id, err := parseItemID(itemID)
		if err != nil {
			return "", err
		}
		it, err := s.storedItem(ctx, id)
		if err != nil {
			return "", err
		}
		access = it.accessToken
	}
	token, err := s.plaid.linkToken(ctx, access, s.redirectURI)
	if err != nil {
		s.log.Warn("link token failed", "error", err)
		return "", errPlaid()
	}
	return token, nil
}

// Connect exchanges the public token from Link for an access token and stores
// the bank. The first sync runs in the background.
func (s *Service) Connect(ctx context.Context, publicToken, institutionID, institutionName string) (Item, error) {
	if !s.Configured() {
		return Item{}, ErrNotConfigured
	}
	if strings.TrimSpace(publicToken) == "" {
		return Item{}, httpx.BadRequest("public_token is required")
	}
	access, plaidID, err := s.plaid.exchange(ctx, publicToken)
	if err != nil {
		s.log.Warn("token exchange failed", "error", err)
		return Item{}, errPlaid()
	}
	institutionID = oneLine(institutionID, 100)
	accounts, err := s.plaid.accounts(ctx, access)
	if err != nil {
		// Without them a repeat link of a connected bank can't be told apart: undo it, to try again.
		s.log.Warn("accounts of a new bank unavailable", "error", err)
		s.revoke(ctx, access)
		return Item{}, errPlaid()
	}
	// Checking for a repeat and saving the bank happen together, so two links at once can't both pass.
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	if dup, err := s.duplicate(ctx, institutionID, plaidID, accounts); err != nil {
		s.revoke(ctx, access)
		return Item{}, err
	} else if dup {
		s.revoke(ctx, access)
		return Item{}, httpx.Conflict("That bank is already connected. To sign in to it again, use Reconnect on it.")
	}
	name := oneLine(institutionName, 100)
	if name == "" {
		name = "Bank"
	}
	// Linking a bank that is already connected replaces its token and starts it afresh.
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO money_items (plaid_item_id, access_token, institution_id, institution_name, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(plaid_item_id) DO UPDATE SET access_token = excluded.access_token,
			institution_id = excluded.institution_id, institution_name = excluded.institution_name,
			status = 'ok', error = ''
		RETURNING id`,
		plaidID, access, institutionID, name, stamp(s.now())).Scan(&id)
	if err != nil {
		return Item{}, fmt.Errorf("store the bank: %w", err)
	}
	if err := s.saveAccounts(ctx, id, accounts); err != nil {
		s.log.Warn("cannot store a new bank's accounts", "item", id, "error", err)
	}
	s.log.Info("bank connected", "item", id)
	s.changed()
	s.syncSoon()
	for _, d := range s.afterConnect {
		time.AfterFunc(d, s.syncSoon)
	}
	return s.itemByID(ctx, id)
}

// revoke removes a just-linked item at Plaid that won't be kept.
func (s *Service) revoke(ctx context.Context, access string) {
	if err := s.plaid.removeItem(context.WithoutCancel(ctx), access); err != nil {
		s.log.Warn("removing a link that isn't kept failed at Plaid", "error", err)
	}
}

// Remove disconnects a bank at Plaid and deletes its accounts and transactions here.
func (s *Service) Remove(ctx context.Context, itemID string) error {
	if !s.Configured() {
		return ErrNotConfigured
	}
	id, err := parseItemID(itemID)
	if err != nil {
		return err
	}
	it, err := s.storedItem(ctx, id)
	if err != nil {
		return err
	}
	// Serialized with syncs, so none is half-way through this bank's rows.
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if err := s.plaid.removeItem(ctx, it.accessToken); err != nil {
		s.log.Warn("bank removal failed at Plaid", "item", id, "error", err)
		return errPlaid()
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM money_items WHERE id = ?`, id); err != nil { // the rows cascade
		return fmt.Errorf("delete the bank: %w", err)
	}
	s.log.Info("bank removed", "item", id)
	s.changed()
	return nil
}
