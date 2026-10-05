package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// legal_entity — migration 000011
// ---------------------------------------------------------------------------

const (
	sqlLegalEntityColumns = `tenant_id, legal_entity_id, name, country_code, is_default, created_at`
	sqlLegalEntityInsert  = `INSERT INTO ztax.legal_entity (` + sqlLegalEntityColumns + `) VALUES ($1, $2, $3, $4, $5, $6)`
	sqlLegalEntityDefault = `SELECT ` + sqlLegalEntityColumns + ` FROM ztax.legal_entity WHERE tenant_id = $1 AND is_default`
	sqlLegalEntityByID    = `SELECT ` + sqlLegalEntityColumns + ` FROM ztax.legal_entity WHERE tenant_id = $1 AND legal_entity_id = $2`
)

// LegalEntityRepo implements port.LegalEntityRepository.
type LegalEntityRepo struct{ s *Store }

// LegalEntities returns the legal-entity repository.
func (s *Store) LegalEntities() *LegalEntityRepo { return &LegalEntityRepo{s: s} }

var _ port.LegalEntityRepository = (*LegalEntityRepo)(nil)

// Create inserts a legal entity. A second default for one tenant is refused by
// the database's unique partial index and surfaces as already existing.
func (r *LegalEntityRepo) Create(ctx context.Context, le identity.LegalEntity) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if le.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The legal entity belongs to a tenant outside the caller's scope.")
	}
	var country *string
	if le.Country != "" {
		country = &le.Country
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlLegalEntityInsert,
		tenant.UUID(), le.ID.UUID(), le.Name, country, le.Default, le.CreatedAt)
	return mapError(err, "insert legal entity")
}

// Default returns the tenant's default legal entity.
func (r *LegalEntityRepo) Default(ctx context.Context) (identity.LegalEntity, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return identity.LegalEntity{}, err
	}
	return scanLegalEntity(r.s.db(ctx).QueryRow(ctx, sqlLegalEntityDefault, tenant.UUID()))
}

// ByID reads one legal entity.
func (r *LegalEntityRepo) ByID(ctx context.Context, legalEntityID id.LegalEntityID) (identity.LegalEntity, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return identity.LegalEntity{}, err
	}
	return scanLegalEntity(r.s.db(ctx).QueryRow(ctx, sqlLegalEntityByID, tenant.UUID(), legalEntityID.UUID()))
}

func scanLegalEntity(row scanner) (identity.LegalEntity, error) {
	var (
		tenantUUID, leUUID uuid.UUID
		country            *string
		le                 identity.LegalEntity
	)
	if err := row.Scan(&tenantUUID, &leUUID, &le.Name, &country, &le.Default, &le.CreatedAt); err != nil {
		return identity.LegalEntity{}, mapError(err, "scan legal entity")
	}
	le.TenantID = id.NewTenantID(tenantUUID)
	le.ID = id.NewLegalEntityID(leUUID)
	if country != nil {
		le.Country = *country
	}
	le.CreatedAt = le.CreatedAt.UTC()
	return le, nil
}

// ---------------------------------------------------------------------------
// tcsl_journal and tcsl_journal_line — migration 000011
// ---------------------------------------------------------------------------

const (
	sqlJournalColumns = `tenant_id, journal_id, legal_entity_id, journal_type, source_kind, source_id,
		posting_date, legal_period, currency, reversal_of, profile_id, profile_version, amendment,
		migration_source, cutover, recorded_at`
	sqlJournalInsert = `INSERT INTO ztax.tcsl_journal (` + sqlJournalColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`
	sqlJournalLineInsert = `INSERT INTO ztax.tcsl_journal_line
		(tenant_id, journal_id, line_no, account, side, amount, currency, authority, jurisdiction,
		 decision_id, document_id, obligation_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	sqlJournalBySource = `SELECT ` + sqlJournalColumns + ` FROM ztax.tcsl_journal
		WHERE tenant_id = $1 AND source_kind = $2 AND source_id = $3 ORDER BY currency, journal_id`
	sqlJournalLines = `SELECT account, side, amount, currency, authority, jurisdiction, decision_id, document_id, obligation_id
		FROM ztax.tcsl_journal_line WHERE tenant_id = $1 AND journal_id = $2 ORDER BY line_no`
	sqlJournalBalances = `SELECT l.account, l.currency, l.side, SUM(l.amount)
		FROM ztax.tcsl_journal_line l
		JOIN ztax.tcsl_journal j ON j.tenant_id = l.tenant_id AND j.journal_id = l.journal_id
		WHERE l.tenant_id = $1 AND j.legal_entity_id = $2
		GROUP BY l.account, l.currency, l.side`
)

// JournalRepo implements port.JournalRepository.
type JournalRepo struct{ s *Store }

// Journals returns the Tax Control Subledger.
func (s *Store) Journals() *JournalRepo { return &JournalRepo{s: s} }

var _ port.JournalRepository = (*JournalRepo)(nil)

// Append writes a journal and its lines in the caller's transaction. The
// journal is validated first: an unbalanced journal is blocked before posting
// (ZTAX-FIN-REQ-0096), here as well as by whoever built it.
func (r *JournalRepo) Append(ctx context.Context, j subledger.Journal) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if j.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The journal belongs to a tenant outside the caller's scope.")
	}
	if err := j.Validate(); err != nil {
		return err
	}
	var reversal *uuid.UUID
	if j.ReversalOf != nil {
		u := j.ReversalOf.UUID()
		reversal = &u
	}
	var migration *string
	if j.MigrationSource != "" {
		migration = &j.MigrationSource
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlJournalInsert,
		tenant.UUID(), j.ID.UUID(), j.LegalEntity.UUID(), string(j.Type), j.Source.Kind, j.Source.ID,
		j.PostingDate, j.LegalPeriod, string(j.Currency), reversal, j.Profile.ID, j.Profile.Version, j.Amendment,
		migration, j.Cutover, time.Now().UTC()); err != nil {
		return mapError(err, "insert journal")
	}
	for i, l := range j.Lines {
		amount, err := MoneyValue(l.Amount)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, sqlJournalLineInsert,
			tenant.UUID(), j.ID.UUID(), i+1, string(l.Account), string(l.Side), amount, string(l.Amount.Currency()),
			optString(l.Authority), optString(l.Jurisdiction),
			optUUID(l.Decision), optUUID(l.Document), optUUID(l.Obligation)); err != nil {
			return mapError(err, "insert journal line")
		}
	}
	return nil
}

// BySource returns the journals one source event posted.
func (r *JournalRepo) BySource(ctx context.Context, kind, sourceID string) ([]subledger.Journal, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlJournalBySource, tenant.UUID(), kind, sourceID)
	if err != nil {
		return nil, mapError(err, "list journals")
	}
	var out []subledger.Journal
	for rows.Next() {
		j, err := scanJournal(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "list journals")
	}
	for i := range out {
		if out[i].Lines, err = r.lines(ctx, tenant, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *JournalRepo) lines(ctx context.Context, tenant id.TenantID, journalID id.JournalID) ([]subledger.JournalLine, error) {
	rows, err := r.s.db(ctx).Query(ctx, sqlJournalLines, tenant.UUID(), journalID.UUID())
	if err != nil {
		return nil, mapError(err, "read journal lines")
	}
	defer rows.Close()
	var out []subledger.JournalLine
	for rows.Next() {
		var (
			account, side, currency     string
			amount                      Numeric
			authority, jurisdiction     *string
			decision, document, obligID *uuid.UUID
		)
		if err := rows.Scan(&account, &side, &amount, &currency, &authority, &jurisdiction, &decision, &document, &obligID); err != nil {
			return nil, mapError(err, "scan journal line")
		}
		m, err := fiscal.MoneyFromNumeric(amount.Parts, fiscal.Currency(currency))
		if err != nil {
			return nil, err
		}
		l := subledger.JournalLine{Account: subledger.Account(account), Side: subledger.Side(side), Amount: m}
		if authority != nil {
			l.Authority = *authority
		}
		if jurisdiction != nil {
			l.Jurisdiction = *jurisdiction
		}
		if decision != nil {
			d := id.NewDecisionID(*decision)
			l.Decision = &d
		}
		if document != nil {
			d := id.NewFiscalDocumentID(*document)
			l.Document = &d
		}
		if obligID != nil {
			o := id.NewObligationID(*obligID)
			l.Obligation = &o
		}
		out = append(out, l)
	}
	return out, mapError(rows.Err(), "read journal lines")
}

func scanJournal(row pgx.Row) (subledger.Journal, error) {
	var (
		tenantUUID, journalUUID, leUUID uuid.UUID
		jtype, currency                 string
		reversal                        *uuid.UUID
		migration                       *string
		cutover                         *time.Time
		recorded                        time.Time
		j                               subledger.Journal
	)
	if err := row.Scan(&tenantUUID, &journalUUID, &leUUID, &jtype, &j.Source.Kind, &j.Source.ID,
		&j.PostingDate, &j.LegalPeriod, &currency, &reversal, &j.Profile.ID, &j.Profile.Version, &j.Amendment,
		&migration, &cutover, &recorded); err != nil {
		return subledger.Journal{}, mapError(err, "scan journal")
	}
	j.TenantID = id.NewTenantID(tenantUUID)
	j.ID = id.NewJournalID(journalUUID)
	j.LegalEntity = id.NewLegalEntityID(leUUID)
	j.Type = subledger.JournalType(jtype)
	j.Currency = fiscal.Currency(currency)
	j.PostingDate = j.PostingDate.UTC()
	if reversal != nil {
		r := id.NewJournalID(*reversal)
		j.ReversalOf = &r
	}
	if migration != nil {
		j.MigrationSource = *migration
	}
	j.Cutover = cutover
	return j, nil
}

// Balances sums a legal entity's posted lines into control balances.
func (r *JournalRepo) Balances(ctx context.Context, legalEntityID id.LegalEntityID) (map[subledger.BalanceKey]subledger.Balance, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlJournalBalances, tenant.UUID(), legalEntityID.UUID())
	if err != nil {
		return nil, mapError(err, "sum journal lines")
	}
	defer rows.Close()
	out := map[subledger.BalanceKey]subledger.Balance{}
	for rows.Next() {
		var (
			account, currency, side string
			sum                     Numeric
		)
		if err := rows.Scan(&account, &currency, &side, &sum); err != nil {
			return nil, mapError(err, "scan balance")
		}
		m, err := fiscal.MoneyFromNumeric(sum.Parts, fiscal.Currency(currency))
		if err != nil {
			return nil, err
		}
		k := subledger.BalanceKey{Account: subledger.Account(account), Currency: fiscal.Currency(currency)}
		b, ok := out[k]
		if !ok {
			b = subledger.Balance{Debits: m.Zero(), Credits: m.Zero()}
		}
		if side == string(subledger.Debit) {
			b.Debits = m
		} else {
			b.Credits = m
		}
		out[k] = b
	}
	return out, mapError(rows.Err(), "sum journal lines")
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type uuider interface{ UUID() uuid.UUID }

func optUUID[T uuider](v *T) *uuid.UUID {
	if v == nil {
		return nil
	}
	u := (*v).UUID()
	return &u
}
