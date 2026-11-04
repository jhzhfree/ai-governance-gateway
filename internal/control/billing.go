package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jhzhfree/ai-governance-gateway/internal/billing"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

var errBillingUnavailable = errors.New("current mainland billing configuration required")

func (s *Store) billingReady(ctx context.Context, tx pgx.Tx) bool {
	for _, m := range s.modelsIn(ctx, tx) {
		if m.Currency == "CNY_MICRO" && m.Provider != "mock" {
			return true
		}
	}
	return false
}

var ErrBillingConflict = errors.New("statement or accounting conflict")

type ImportInput struct {
	Provider    string `json:"provider"`
	AccountRef  string `json:"account_ref"`
	EvidenceRef string `json:"evidence_ref"`
	CSV         string `json:"csv"`
}
type ImportReceipt struct {
	ID        string `json:"id"`
	Count     int    `json:"count"`
	Duplicate bool   `json:"duplicate"`
}
type BillingRecord struct {
	ID                string              `json:"id"`
	ImportID          string              `json:"import_id"`
	Provider          string              `json:"provider"`
	AccountRef        string              `json:"account_ref"`
	RecordID          string              `json:"record_id"`
	RequestID         string              `json:"request_id"`
	Period            string              `json:"period"`
	Currency          string              `json:"currency"`
	Amount            string              `json:"amount"`
	InputTokens       int64               `json:"input_tokens"`
	OutputTokens      int64               `json:"output_tokens"`
	Decision          string              `json:"decision"`
	Revision          int                 `json:"revision"`
	ResolvedRequestID *string             `json:"resolved_request_id"`
	Reviewer          *identity.Principal `json:"reviewer"`
	Reason            *string             `json:"reason"`
	ReviewedAt        *time.Time          `json:"reviewed_at"`
	CreatedAt         time.Time           `json:"created_at"`
	Importer          identity.Principal  `json:"importer"`
	EvidenceRef       string              `json:"evidence_ref"`
	Issues            []string            `json:"issues"`
	LedgerState       string              `json:"ledger_state"`
	Reserved          *string             `json:"reserved"`
	Actual            *string             `json:"actual"`
}
type ReviewInput struct {
	Action           string `json:"action"`
	RequestID        string `json:"request_id"`
	ExpectedRevision int    `json:"expected_revision"`
	ConfirmedFinal   bool   `json:"confirmed_final"`
	Reason           string `json:"reason"`
}

func billingError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrBillingConflict
	}
	return err
}
func (s *Store) ImportStatement(ctx context.Context, p identity.Principal, org, project string, in ImportInput) (v ImportReceipt, err error) {
	if (in.Provider != "aliyun" && in.Provider != "volcengine" && in.Provider != "zhipu") || !billing.Identifier(in.AccountRef) || !billing.Identifier(in.EvidenceRef) {
		return v, ErrInvalid
	}
	lines, e := billing.Parse(in.CSV)
	if e != nil {
		return v, ErrInvalid
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].RecordID < lines[j].RecordID })
	canonical, _ := json.Marshal(struct {
		Provider, Account, Evidence string
		Lines                       []billing.Line
	}{in.Provider, in.AccountRef, in.EvidenceRef, lines})
	hash := sha256.Sum256(canonical)
	digest := hex.EncodeToString(hash[:])
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if !s.billingReady(ctx, tx) {
			return errBillingUnavailable
		}
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		var currency string
		if err := tx.QueryRow(ctx, `SELECT currency FROM organizations WHERE id=$1`, org).Scan(&currency); err != nil {
			return err
		}
		if currency != "CNY_MICRO" {
			return ErrInvalid
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aigw:billing-import:"+org+":"+project+":"+digest); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT id FROM billing_imports WHERE organization_id=$1 AND project_id=$2 AND digest=$3`, org, project, digest).Scan(&v.ID)
		if err == nil {
			v.Count = len(lines)
			v.Duplicate = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		v.ID = rand.Text()
		v.Count = len(lines)
		if _, err := tx.Exec(ctx, `INSERT INTO billing_imports(id,organization_id,project_id,provider,account_ref,evidence_ref,digest,importer_issuer,importer_subject) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, v.ID, org, project, in.Provider, in.AccountRef, in.EvidenceRef, digest, p.Issuer, p.Subject); err != nil {
			return err
		}
		for _, line := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO billing_records(id,import_id,organization_id,project_id,provider,account_ref,record_id,suggested_request_id,period_id,currency,amount,input_tokens,output_tokens) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, rand.Text(), v.ID, org, project, in.Provider, in.AccountRef, line.RecordID, line.RequestID, line.Period, line.Currency, line.Amount, line.InputTokens, line.OutputTokens); err != nil {
				return err
			}
		}
		return audit(ctx, tx, org, p, "billing.imported", v.ID)
	})
	return v, billingError(err)
}

const billingColumns = `b.id,b.import_id,b.provider,b.account_ref,b.record_id,b.suggested_request_id,b.period_id,b.currency,b.amount::text,b.input_tokens,b.output_tokens,b.decision,b.revision,b.resolved_request_id,b.reviewer_issuer,b.reviewer_subject,b.reason,b.reviewed_at,b.created_at,i.importer_issuer,i.importer_subject,i.evidence_ref`

func scanBill(row interface{ Scan(...any) error }) (v BillingRecord, err error) {
	var issuer, subject *string
	v.Issues = []string{}
	err = row.Scan(&v.ID, &v.ImportID, &v.Provider, &v.AccountRef, &v.RecordID, &v.RequestID, &v.Period, &v.Currency, &v.Amount, &v.InputTokens, &v.OutputTokens, &v.Decision, &v.Revision, &v.ResolvedRequestID, &issuer, &subject, &v.Reason, &v.ReviewedAt, &v.CreatedAt, &v.Importer.Issuer, &v.Importer.Subject, &v.EvidenceRef)
	if issuer != nil && subject != nil {
		v.Reviewer = &identity.Principal{Issuer: *issuer, Subject: *subject}
	}
	return
}

// matchBill requires an exact operator-supplied gateway ID; no fuzzy matching.
func matchBill(ctx context.Context, tx pgx.Tx, org, project, id string, v *BillingRecord) error {
	var period, currency string
	var reserved int64
	var actual *int64
	var supplier *string
	var active, recent bool
	var input, output *int64
	err := tx.QueryRow(ctx, `SELECT r.state,r.period_id,r.currency,r.reserved,r.actual,a.provider,a.input_tokens,a.output_tokens,EXISTS(SELECT 1 FROM admission_leases l WHERE l.request_id=r.request_id AND l.expires_at>clock_timestamp()),r.updated_at>clock_timestamp()-interval '5 minutes' FROM budget_reservations r LEFT JOIN inference_attempts a USING(request_id) WHERE r.organization_id=$1 AND r.project_id=$2 AND r.request_id=$3`, org, project, id).Scan(&v.LedgerState, &period, &currency, &reserved, &actual, &supplier, &input, &output, &active, &recent)
	if errors.Is(err, pgx.ErrNoRows) {
		v.Issues = append(v.Issues, "missing_request")
		return nil
	}
	if err != nil {
		return err
	}
	r := fmt.Sprint(reserved)
	v.Reserved = &r
	if actual != nil {
		s := fmt.Sprint(*actual)
		v.Actual = &s
	}
	if supplier == nil {
		v.Issues = append(v.Issues, "provider_unrecorded")
	} else if *supplier != v.Provider {
		v.Issues = append(v.Issues, "provider_mismatch")
	}
	if period != v.Period || currency != v.Currency {
		v.Issues = append(v.Issues, "scope_mismatch")
	}
	if (input != nil && *input != v.InputTokens) || (output != nil && *output != v.OutputTokens) {
		v.Issues = append(v.Issues, "usage_mismatch")
	}
	if v.Decision == "pending" {
		var duplicate bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_records b WHERE b.organization_id=$1 AND b.project_id=$2 AND b.id<>$3 AND b.decision<>'rejected' AND COALESCE(b.resolved_request_id,b.suggested_request_id)=$4)`, org, project, v.ID, id).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate {
			v.Issues = append(v.Issues, "duplicate_request")
		}
	}
	if actual != nil && fmt.Sprint(*actual) != v.Amount {
		v.Issues = append(v.Issues, "amount_mismatch")
	}
	var amount int64
	if _, err := fmt.Sscan(v.Amount, &amount); err != nil {
		return err
	}
	if amount > reserved {
		v.Issues = append(v.Issues, "over_bound")
	}
	if v.LedgerState == "dispatched" || v.LedgerState == "uncertain" {
		v.Issues = append(v.Issues, "unsettled")
		if active {
			v.Issues = append(v.Issues, "active_execution")
		}
		if recent {
			v.Issues = append(v.Issues, "recent_execution")
		}
	} else if v.LedgerState != "settled" {
		v.Issues = append(v.Issues, "unsupported_state")
	}
	return nil
}
func validBillDecision(s string) bool {
	return s == "" || s == "pending" || s == "approved" || s == "rejected"
}
func (s *Store) BillingRecords(ctx context.Context, p identity.Principal, org, project, after, decision string, limit int) (items []BillingRecord, next string, err error) {
	items = []BillingRecord{}
	stamp, id, e := requestCursor(after)
	if e != nil || !validBillDecision(decision) || limit < 1 || limit > 100 {
		return items, "", ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if !s.billingReady(ctx, tx) {
			return errBillingUnavailable
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+billingColumns+` FROM billing_records b JOIN billing_imports i ON i.id=b.import_id WHERE b.organization_id=$1 AND b.project_id=$2 AND ($3='' OR b.decision=$3) AND ($4='' OR (b.created_at,b.id)<($5,$4)) ORDER BY b.created_at DESC,b.id DESC LIMIT $6`, org, project, decision, id, stamp, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			v, err := scanBill(rows)
			if err != nil {
				rows.Close()
				return err
			}
			items = append(items, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(items) > limit {
			items = items[:limit]
			last := items[len(items)-1]
			next = (RequestRecord{RequestID: last.ID, CreatedAt: last.CreatedAt}).Cursor()
		}
		for i := range items {
			id := items[i].RequestID
			if items[i].ResolvedRequestID != nil {
				id = *items[i].ResolvedRequestID
			}
			if err := matchBill(ctx, tx, org, project, id, &items[i]); err != nil {
				return err
			}
		}
		return nil
	})
	return
}
func (s *Store) ReviewBill(ctx context.Context, p identity.Principal, org, project, id string, in ReviewInput) (v BillingRecord, err error) {
	if !billing.Identifier(id) || (in.Action != "approve" && in.Action != "reject") || in.ExpectedRevision < 0 || in.ExpectedRevision > 1 || strings.TrimSpace(in.Reason) != in.Reason || len(in.Reason) < 1 || len(in.Reason) > 256 || (in.Action == "approve" && (!in.ConfirmedFinal || !billing.Identifier(in.RequestID))) {
		return v, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if !s.billingReady(ctx, tx) {
			return errBillingUnavailable
		}
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		var e error
		v, e = scanBill(tx.QueryRow(ctx, `SELECT `+billingColumns+` FROM billing_records b JOIN billing_imports i ON i.id=b.import_id WHERE b.organization_id=$1 AND b.project_id=$2 AND b.id=$3 FOR UPDATE OF b`, org, project, id))
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		target := "rejected"
		if in.Action == "approve" {
			target = "approved"
		}
		if v.Decision != "pending" {
			if v.Decision == target && v.Reviewer != nil && *v.Reviewer == p && v.Reason != nil && *v.Reason == in.Reason && (target == "rejected" || (v.ResolvedRequestID != nil && *v.ResolvedRequestID == in.RequestID)) {
				targetID := v.RequestID
				if v.ResolvedRequestID != nil {
					targetID = *v.ResolvedRequestID
				}
				return matchBill(ctx, tx, org, project, targetID, &v)
			}
			return ErrBillingConflict
		}
		if v.Revision != in.ExpectedRevision {
			return ErrBillingConflict
		}
		if target == "approved" {
			if v.Importer == p {
				return identity.ErrForbidden
			}
			// Share the ledger's per-request lock before rechecking the mutable balance.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, in.RequestID); err != nil {
				return err
			}
			if err := matchBill(ctx, tx, org, project, in.RequestID, &v); err != nil {
				return err
			}
			for _, issue := range v.Issues {
				if issue != "unsettled" && issue != "usage_mismatch" {
					return ErrBillingConflict
				}
			}
			var amount int64
			if _, err := fmt.Sscan(v.Amount, &amount); err != nil {
				return err
			}
			if err := budget.ReconcileTx(ctx, tx, in.RequestID, amount); err != nil {
				if errors.Is(err, budget.ErrState) || errors.Is(err, budget.ErrCostBound) {
					return ErrBillingConflict
				}
				return err
			}
		}
		var resolved *string
		if target == "approved" {
			resolved = &in.RequestID
		}
		if _, err := tx.Exec(ctx, `UPDATE billing_records SET decision=$2,revision=1,resolved_request_id=$3,reviewer_issuer=$4,reviewer_subject=$5,reason=$6,reviewed_at=clock_timestamp() WHERE id=$1`, id, target, resolved, p.Issuer, p.Subject, in.Reason); err != nil {
			return err
		}
		if err := audit(ctx, tx, org, p, "billing."+target, id); err != nil {
			return err
		}
		v, e = scanBill(tx.QueryRow(ctx, `SELECT `+billingColumns+` FROM billing_records b JOIN billing_imports i ON i.id=b.import_id WHERE b.id=$1`, id))
		if e != nil {
			return e
		}
		targetID := v.RequestID
		if v.ResolvedRequestID != nil {
			targetID = *v.ResolvedRequestID
		}
		return matchBill(ctx, tx, org, project, targetID, &v)
	})
	return v, billingError(err)
}
