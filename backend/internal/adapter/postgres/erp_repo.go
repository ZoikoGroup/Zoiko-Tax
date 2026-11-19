package postgres

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/erp"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// erp_* — migration 000034
// ---------------------------------------------------------------------------

const (
	sqlERPBridgeColumns = `bridge_id, legal_entity_id, name, vendor, company_code, created_at, created_by`
	sqlERPBridgeInsert  = `INSERT INTO ztax.erp_bridge (tenant_id, ` + sqlERPBridgeColumns + `) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlERPBridgeSelect  = `SELECT ` + sqlERPBridgeColumns + ` FROM ztax.erp_bridge WHERE tenant_id = $1`
	sqlERPBridgeByID    = sqlERPBridgeSelect + ` AND bridge_id = $2`
	sqlERPBridgeAll     = sqlERPBridgeSelect + ` ORDER BY created_at, bridge_id`

	sqlERPMappingInsert = `INSERT INTO ztax.erp_account_mapping (tenant_id, bridge_id, version, chart_version, digest, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	sqlERPMappingLineInsert = `INSERT INTO ztax.erp_account_mapping_line (tenant_id, bridge_id, version, tcsl_account, gl_account)
		VALUES ($1, $2, $3, $4, $5)`
	sqlERPMappings = `SELECT bridge_id, version, chart_version, digest, recorded_at, recorded_by FROM ztax.erp_account_mapping
		WHERE tenant_id = $1 AND bridge_id = ANY($2) ORDER BY bridge_id, version`
	sqlERPMappingLines = `SELECT bridge_id, version, tcsl_account, gl_account FROM ztax.erp_account_mapping_line
		WHERE tenant_id = $1 AND bridge_id = ANY($2) ORDER BY bridge_id, version, tcsl_account`

	sqlERPBatchColumns = `batch_id, bridge_id, legal_entity_id, legal_period, currency, posting_date, company_code, mapping_version,
		chart_version, mapping_digest, total_debits, total_credits, batch_digest, prepared_at, prepared_by`
	sqlERPBatchInsert = `INSERT INTO ztax.erp_export_batch (tenant_id, ` + sqlERPBatchColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`
	sqlERPBatchSelect = `SELECT ` + sqlERPBatchColumns + ` FROM ztax.erp_export_batch WHERE tenant_id = $1`
	sqlERPBatchByID   = sqlERPBatchSelect + ` AND batch_id = $2`
	// Every filter is a parameter; an empty one matches everything. The
	// status filter reads the latest event, which is the folded status.
	sqlERPBatchList = sqlERPBatchSelect + ` AND bridge_id = $2
		AND ($3 = '' OR legal_period = $3)
		AND ($4 = '' OR currency = $4)
		AND ($5 = '' OR (SELECT e.status_after FROM ztax.erp_export_event e
			WHERE e.tenant_id = erp_export_batch.tenant_id AND e.batch_id = erp_export_batch.batch_id
			ORDER BY e.seq DESC LIMIT 1) = $5)
		ORDER BY prepared_at DESC, batch_id DESC LIMIT $6`
	sqlERPLineInsert = `INSERT INTO ztax.erp_export_line (tenant_id, batch_id, line_no, journal_id, journal_line_no, tcsl_account,
		gl_account, side, amount, authority, jurisdiction) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	sqlERPLines = `SELECT batch_id, journal_id, journal_line_no, tcsl_account, gl_account, side, amount, authority, jurisdiction
		FROM ztax.erp_export_line WHERE tenant_id = $1 AND batch_id = ANY($2) ORDER BY batch_id, line_no`
	sqlERPEventInsert = `INSERT INTO ztax.erp_export_event (tenant_id, batch_id, seq, kind, status_after, attempt_id,
		remote_correlation, erp_reference, response_digest, detail, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	sqlERPEvents = `SELECT batch_id, seq, kind, status_after, attempt_id, remote_correlation, erp_reference, response_digest,
		detail, recorded_at, recorded_by FROM ztax.erp_export_event WHERE tenant_id = $1 AND batch_id = ANY($2)
		ORDER BY batch_id, seq`

	sqlERPPeriodJournals = `SELECT ` + sqlJournalColumns + ` FROM ztax.tcsl_journal
		WHERE tenant_id = $1 AND legal_entity_id = $2 AND legal_period = $3 AND currency = $4 ORDER BY journal_id`
	sqlERPPeriodJournalLines = `SELECT journal_id, account, side, amount, currency, authority, jurisdiction, decision_id,
		document_id, obligation_id FROM ztax.tcsl_journal_line WHERE tenant_id = $1 AND journal_id = ANY($2)
		ORDER BY journal_id, line_no`

	sqlERPSnapshotColumns = `snapshot_id, bridge_id, legal_period, currency, as_of, source_ref, source_digest, digest, recorded_at, recorded_by`
	sqlERPSnapshotInsert  = `INSERT INTO ztax.erp_gl_snapshot (tenant_id, ` + sqlERPSnapshotColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	sqlERPSnapshotLineInsert = `INSERT INTO ztax.erp_gl_snapshot_line (tenant_id, snapshot_id, gl_account, debits, credits)
		VALUES ($1, $2, $3, $4, $5)`
	sqlERPSnapshotSelect = `SELECT ` + sqlERPSnapshotColumns + ` FROM ztax.erp_gl_snapshot WHERE tenant_id = $1`
	sqlERPSnapshotByID   = sqlERPSnapshotSelect + ` AND snapshot_id = $2`
	sqlERPSnapshotList   = sqlERPSnapshotSelect + ` AND bridge_id = $2 AND ($3 = '' OR legal_period = $3)
		ORDER BY recorded_at DESC, snapshot_id DESC LIMIT $4`
	sqlERPSnapshotLines = `SELECT snapshot_id, gl_account, debits, credits FROM ztax.erp_gl_snapshot_line
		WHERE tenant_id = $1 AND snapshot_id = ANY($2) ORDER BY snapshot_id, gl_account`

	sqlERPReconColumns = `reconciliation_id, bridge_id, snapshot_id, snapshot_digest, legal_entity_id, legal_period, currency,
		mapping_version, outcome, holds, journal_count, posted_journal_count, population_digest, ran_at, ran_by`
	sqlERPReconInsert = `INSERT INTO ztax.erp_gl_reconciliation (tenant_id, ` + sqlERPReconColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`
	sqlERPReconList = `SELECT ` + sqlERPReconColumns + ` FROM ztax.erp_gl_reconciliation WHERE tenant_id = $1 AND bridge_id = $2
		AND ($3 = '' OR legal_period = $3) ORDER BY ran_at DESC, reconciliation_id DESC LIMIT $4`
	sqlERPReconItemInsert = `INSERT INTO ztax.erp_gl_reconciliation_item (tenant_id, reconciliation_id, ordinal, match_key,
		gl_account, tcsl_accounts, expected, observed, variance, status, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	sqlERPReconItems = `SELECT reconciliation_id, match_key, gl_account, tcsl_accounts, expected, observed, variance, status, detail
		FROM ztax.erp_gl_reconciliation_item WHERE tenant_id = $1 AND reconciliation_id = ANY($2)
		ORDER BY reconciliation_id, ordinal`

	sqlERPLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	erpListCap = 500
)

// ERPRepo implements port.ERPRepository.
type ERPRepo struct{ s *Store }

// ERP returns the ERP bridge repository.
func (s *Store) ERP() *ERPRepo { return &ERPRepo{s: s} }

var _ port.ERPRepository = (*ERPRepo)(nil)

func erpFault(err error, msg string) error { return obligationFault(err, msg) }

// Lock takes one of the tenant's ERP locks for the transaction.
func (r *ERPRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlERPLock, tenant.String()+"/erp/"+key)
	return mapError(err, "lock erp")
}

// CreateBridge writes a bridge.
func (r *ERPRepo) CreateBridge(ctx context.Context, b erp.Bridge) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if b.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The bridge belongs to a tenant outside the caller's scope.")
	}
	if err := b.Validate(); err != nil {
		return erpFault(err, "The bridge is not well-formed.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlERPBridgeInsert, tenant.UUID(), b.ID.UUID(), b.LegalEntity.UUID(), b.Name, string(b.Vendor),
		b.CompanyCode, b.CreatedAt.UTC(), b.CreatedBy.UUID())
	return mapError(err, "insert erp bridge")
}

// AppendMapping writes a mapping version and its lines.
func (r *ERPRepo) AppendMapping(ctx context.Context, bridge id.ERPBridgeID, m erp.Mapping) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return erpFault(err, "The mapping is not well-formed.")
	}
	if m.Digest.IsZero() {
		return erpFault(nil, "A mapping is stored with its digest.")
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlERPMappingInsert, tenant.UUID(), bridge.UUID(), m.Version, m.ChartVersion, m.Digest.String(),
		m.RecordedAt.UTC(), m.RecordedBy.UUID()); err != nil {
		return mapError(err, "insert erp mapping")
	}
	for _, am := range m.Accounts {
		if _, err := db.Exec(ctx, sqlERPMappingLineInsert, tenant.UUID(), bridge.UUID(), m.Version, string(am.Account), am.GLAccount); err != nil {
			return mapError(err, "insert erp mapping line")
		}
	}
	return nil
}

// Bridge returns one bridge with its mapping versions.
func (r *ERPRepo) Bridge(ctx context.Context, bridge id.ERPBridgeID) (erp.Bridge, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return erp.Bridge{}, err
	}
	got, err := r.bridges(ctx, sqlERPBridgeByID, tenant.UUID(), bridge.UUID())
	if err != nil {
		return erp.Bridge{}, err
	}
	if len(got) == 0 {
		return erp.Bridge{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such ERP bridge.")
	}
	return got[0], nil
}

// Bridges returns the tenant's bridges.
func (r *ERPRepo) Bridges(ctx context.Context) ([]erp.Bridge, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return r.bridges(ctx, sqlERPBridgeAll, tenant.UUID())
}

func (r *ERPRepo) bridges(ctx context.Context, query string, args ...any) ([]erp.Bridge, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, mapError(err, "read erp bridges")
	}
	var out []erp.Bridge
	for rows.Next() {
		var (
			b               = erp.Bridge{TenantID: tenant}
			bid, entity, by uuid.UUID
			vendor          string
		)
		if err := rows.Scan(&bid, &entity, &b.Name, &vendor, &b.CompanyCode, &b.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan erp bridge")
		}
		b.ID, b.LegalEntity, b.CreatedBy = id.NewERPBridgeID(bid), id.NewLegalEntityID(entity), id.NewUserID(by)
		b.Vendor, b.CreatedAt = erp.Vendor(vendor), b.CreatedAt.UTC()
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read erp bridges")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, b := range out {
		ids[i] = b.ID.UUID()
	}

	type key struct {
		bridge  uuid.UUID
		version int
	}
	lines := map[key][]erp.AccountMap{}
	rows, err = db.Query(ctx, sqlERPMappingLines, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read erp mapping lines")
	}
	for rows.Next() {
		var (
			k           key
			account, gl string
		)
		if err := rows.Scan(&k.bridge, &k.version, &account, &gl); err != nil {
			rows.Close()
			return nil, mapError(err, "scan erp mapping line")
		}
		lines[k] = append(lines[k], erp.AccountMap{Account: subledger.Account(account), GLAccount: gl})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read erp mapping lines")
	}

	rows, err = db.Query(ctx, sqlERPMappings, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read erp mappings")
	}
	defer rows.Close()
	mappings := map[uuid.UUID][]erp.Mapping{}
	for rows.Next() {
		var (
			m       erp.Mapping
			bid, by uuid.UUID
			digest  string
		)
		if err := rows.Scan(&bid, &m.Version, &m.ChartVersion, &digest, &m.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan erp mapping")
		}
		if m.Digest, err = canonical.ParseDigest(digest); err != nil {
			return nil, erpFault(err, "A stored mapping's digest does not parse.")
		}
		m.RecordedAt, m.RecordedBy = m.RecordedAt.UTC(), id.NewUserID(by)
		// Sorted in Go, as the digest was taken: a database collation does
		// not order account codes the way the service compares them.
		m.Accounts = lines[key{bid, m.Version}]
		m = m.Normalise()
		mappings[bid] = append(mappings[bid], m)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read erp mappings")
	}
	for i := range out {
		out[i].Mappings = mappings[out[i].ID.UUID()]
	}
	return out, nil
}

// CreateBatch writes a sealed batch, its lines and its PREPARED event.
func (r *ERPRepo) CreateBatch(ctx context.Context, b erp.Batch, prepared erp.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if b.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The batch belongs to a tenant outside the caller's scope.")
	}
	if err := b.Verify(); err != nil {
		return erpFault(err, "A batch is stored with the digest of its own contents.")
	}
	if prepared.Kind != erp.EventPrepared || prepared.Seq != 1 {
		return erpFault(nil, "A batch is created by its PREPARED event.")
	}
	debits, err := MoneyValue(b.TotalDebits)
	if err != nil {
		return err
	}
	credits, err := MoneyValue(b.TotalCredits)
	if err != nil {
		return err
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlERPBatchInsert, tenant.UUID(), b.ID.UUID(), b.Bridge.UUID(), b.LegalEntity.UUID(), b.Period,
		string(b.Currency), b.PostingDate, b.CompanyCode, b.MappingVersion, b.ChartVersion, b.MappingDigest.String(),
		debits, credits, b.Digest.String(), b.PreparedAt.UTC(), b.PreparedBy.UUID()); err != nil {
		return mapError(err, "insert erp export batch")
	}
	for i, l := range b.Lines {
		amount, err := MoneyValue(l.Amount)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, sqlERPLineInsert, tenant.UUID(), b.ID.UUID(), i+1, l.Journal.UUID(), l.JournalLine,
			string(l.Account), l.GLAccount, string(l.Side), amount, optString(l.Authority), optString(l.Jurisdiction)); err != nil {
			return mapError(err, "insert erp export line")
		}
	}
	return r.AppendBatchEvent(ctx, b.ID, prepared)
}

// AppendBatchEvent writes one batch event.
func (r *ERPRepo) AppendBatchEvent(ctx context.Context, batch id.ERPExportID, e erp.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var attempt *uuid.UUID
	if !e.Attempt.IsZero() {
		u := e.Attempt.UUID()
		attempt = &u
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlERPEventInsert, tenant.UUID(), batch.UUID(), e.Seq, string(e.Kind), string(e.Status), attempt,
		optString(e.RemoteCorrelation), optString(e.ERPReference), optString(e.ResponseDigest), optString(e.Detail),
		e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert erp export event")
}

// Batch returns one batch.
func (r *ERPRepo) Batch(ctx context.Context, batch id.ERPExportID) (erp.Batch, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return erp.Batch{}, err
	}
	got, err := r.batches(ctx, sqlERPBatchByID, tenant.UUID(), batch.UUID())
	if err != nil {
		return erp.Batch{}, err
	}
	if len(got) == 0 {
		return erp.Batch{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such ERP export batch.")
	}
	return got[0], nil
}

// Batches returns a bridge's batches, newest first.
func (r *ERPRepo) Batches(ctx context.Context, bridge id.ERPBridgeID, f erp.Filter) ([]erp.Batch, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return r.batches(ctx, sqlERPBatchList, tenant.UUID(), bridge.UUID(), f.Period, string(f.Currency), string(f.Status), erpListCap)
}

// batches reads the batches a fixed query from this file selects, with
// their lines, and folds each with its events.
func (r *ERPRepo) batches(ctx context.Context, query string, args ...any) ([]erp.Batch, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, mapError(err, "read erp export batches")
	}
	var out []erp.Batch
	for rows.Next() {
		var (
			b                               = erp.Batch{TenantID: tenant}
			bid, bridge, entity, by         uuid.UUID
			currency, mappingDigest, digest string
			debits, credits                 Numeric
		)
		if err := rows.Scan(&bid, &bridge, &entity, &b.Period, &currency, &b.PostingDate, &b.CompanyCode, &b.MappingVersion,
			&b.ChartVersion, &mappingDigest, &debits, &credits, &digest, &b.PreparedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan erp export batch")
		}
		b.Currency = fiscal.Currency(currency)
		if b.TotalDebits, err = debits.Money(b.Currency); err == nil {
			b.TotalCredits, err = credits.Money(b.Currency)
		}
		if err == nil {
			b.MappingDigest, err = canonical.ParseDigest(mappingDigest)
		}
		if err == nil {
			b.Digest, err = canonical.ParseDigest(digest)
		}
		if err != nil {
			rows.Close()
			return nil, erpFault(err, "A stored export batch does not read back.")
		}
		b.ID, b.Bridge, b.LegalEntity, b.PreparedBy = id.NewERPExportID(bid), id.NewERPBridgeID(bridge), id.NewLegalEntityID(entity), id.NewUserID(by)
		pd := b.PostingDate
		b.PostingDate, b.PreparedAt = time.Date(pd.Year(), pd.Month(), pd.Day(), 0, 0, 0, 0, time.UTC), b.PreparedAt.UTC()
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read erp export batches")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	index := map[id.ERPExportID]int{}
	for i, b := range out {
		ids[i], index[b.ID] = b.ID.UUID(), i
	}

	rows, err = db.Query(ctx, sqlERPLines, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read erp export lines")
	}
	journals := make([]map[id.JournalID]bool, len(out))
	for rows.Next() {
		var (
			l                       erp.Line
			bid, journal            uuid.UUID
			account, side           string
			amount                  Numeric
			authority, jurisdiction *string
		)
		if err := rows.Scan(&bid, &journal, &l.JournalLine, &account, &l.GLAccount, &side, &amount, &authority, &jurisdiction); err != nil {
			rows.Close()
			return nil, mapError(err, "scan erp export line")
		}
		i := index[id.NewERPExportID(bid)]
		if l.Amount, err = amount.Money(out[i].Currency); err != nil {
			rows.Close()
			return nil, err
		}
		l.Journal, l.Account, l.Side = id.NewJournalID(journal), subledger.Account(account), subledger.Side(side)
		l.Authority, l.Jurisdiction = deref(authority), deref(jurisdiction)
		out[i].Lines = append(out[i].Lines, l)
		if journals[i] == nil {
			journals[i] = map[id.JournalID]bool{}
		}
		if !journals[i][l.Journal] {
			journals[i][l.Journal] = true
			out[i].Journals = append(out[i].Journals, l.Journal)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read erp export lines")
	}

	rows, err = db.Query(ctx, sqlERPEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read erp export events")
	}
	defer rows.Close()
	events := map[id.ERPExportID][]erp.Event{}
	for rows.Next() {
		var (
			e                                      erp.Event
			bid, by                                uuid.UUID
			attempt                                *uuid.UUID
			kind, status                           string
			correlation, reference, digest, detail *string
		)
		if err := rows.Scan(&bid, &e.Seq, &kind, &status, &attempt, &correlation, &reference, &digest, &detail,
			&e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan erp export event")
		}
		e.Kind, e.Status, e.RecordedAt, e.RecordedBy = erp.EventKind(kind), erp.Status(status), e.RecordedAt.UTC(), id.NewUserID(by)
		if attempt != nil {
			e.Attempt = id.NewERPAttemptID(*attempt)
		}
		e.RemoteCorrelation, e.ERPReference, e.ResponseDigest, e.Detail = deref(correlation), deref(reference), deref(digest), deref(detail)
		k := id.NewERPExportID(bid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read erp export events")
	}
	for i := range out {
		// The journal ID set is sorted, as it was sealed.
		slices.SortFunc(out[i].Journals, func(a, b id.JournalID) int { return strings.Compare(a.String(), b.String()) })
		folded, err := erp.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, erpFault(err, "A stored export batch's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

// PeriodJournals returns the posted journals of a legal entity, period and
// currency, with their lines.
func (r *ERPRepo) PeriodJournals(ctx context.Context, legalEntity id.LegalEntityID, period string, currency fiscal.Currency) ([]subledger.Journal, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlERPPeriodJournals, tenant.UUID(), legalEntity.UUID(), period, string(currency))
	if err != nil {
		return nil, mapError(err, "read period journals")
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
		return nil, mapError(err, "read period journals")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	index := map[uuid.UUID]int{}
	for i, j := range out {
		ids[i], index[j.ID.UUID()] = j.ID.UUID(), i
	}
	rows, err = db.Query(ctx, sqlERPPeriodJournalLines, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read period journal lines")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			journal                     uuid.UUID
			account, side, cur          string
			amount                      Numeric
			authority, jurisdiction     *string
			decision, document, obligID *uuid.UUID
		)
		if err := rows.Scan(&journal, &account, &side, &amount, &cur, &authority, &jurisdiction, &decision, &document, &obligID); err != nil {
			return nil, mapError(err, "scan period journal line")
		}
		m, err := amount.Money(fiscal.Currency(cur))
		if err != nil {
			return nil, err
		}
		l := subledger.JournalLine{Account: subledger.Account(account), Side: subledger.Side(side), Amount: m,
			Authority: deref(authority), Jurisdiction: deref(jurisdiction)}
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
		i := index[journal]
		out[i].Lines = append(out[i].Lines, l)
	}
	return out, mapError(rows.Err(), "read period journal lines")
}

// RecordSnapshot writes a snapshot and its lines.
func (r *ERPRepo) RecordSnapshot(ctx context.Context, s erp.GLSnapshot) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if s.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The snapshot belongs to a tenant outside the caller's scope.")
	}
	if err := s.Validate(); err != nil {
		return erpFault(err, "The snapshot is not well-formed.")
	}
	if s.Digest.IsZero() {
		return erpFault(nil, "A snapshot is stored with its digest.")
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlERPSnapshotInsert, tenant.UUID(), s.ID.UUID(), s.Bridge.UUID(), s.Period, string(s.Currency),
		s.AsOf.UTC(), s.SourceRef, optString(s.SourceDigest), s.Digest.String(), s.RecordedAt.UTC(), s.RecordedBy.UUID()); err != nil {
		return mapError(err, "insert erp gl snapshot")
	}
	for _, l := range s.Lines {
		d, err := MoneyValue(l.Debits)
		if err != nil {
			return err
		}
		c, err := MoneyValue(l.Credits)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, sqlERPSnapshotLineInsert, tenant.UUID(), s.ID.UUID(), l.GLAccount, d, c); err != nil {
			return mapError(err, "insert erp gl snapshot line")
		}
	}
	return nil
}

// Snapshot returns one snapshot.
func (r *ERPRepo) Snapshot(ctx context.Context, snapshot id.GLSnapshotID) (erp.GLSnapshot, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return erp.GLSnapshot{}, err
	}
	got, err := r.snapshots(ctx, sqlERPSnapshotByID, tenant.UUID(), snapshot.UUID())
	if err != nil {
		return erp.GLSnapshot{}, err
	}
	if len(got) == 0 {
		return erp.GLSnapshot{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such GL balance snapshot.")
	}
	return got[0], nil
}

// Snapshots returns a bridge's snapshots, newest first.
func (r *ERPRepo) Snapshots(ctx context.Context, bridge id.ERPBridgeID, period string) ([]erp.GLSnapshot, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return r.snapshots(ctx, sqlERPSnapshotList, tenant.UUID(), bridge.UUID(), period, erpListCap)
}

func (r *ERPRepo) snapshots(ctx context.Context, query string, args ...any) ([]erp.GLSnapshot, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, mapError(err, "read erp gl snapshots")
	}
	var out []erp.GLSnapshot
	for rows.Next() {
		var (
			s                = erp.GLSnapshot{TenantID: tenant}
			sid, bridge, by  uuid.UUID
			currency, digest string
			sourceDigest     *string
		)
		if err := rows.Scan(&sid, &bridge, &s.Period, &currency, &s.AsOf, &s.SourceRef, &sourceDigest, &digest, &s.RecordedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan erp gl snapshot")
		}
		if s.Digest, err = canonical.ParseDigest(digest); err != nil {
			rows.Close()
			return nil, erpFault(err, "A stored snapshot's digest does not parse.")
		}
		s.ID, s.Bridge, s.RecordedBy = id.NewGLSnapshotID(sid), id.NewERPBridgeID(bridge), id.NewUserID(by)
		s.Currency, s.SourceDigest, s.AsOf, s.RecordedAt = fiscal.Currency(currency), deref(sourceDigest), s.AsOf.UTC(), s.RecordedAt.UTC()
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read erp gl snapshots")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	index := map[uuid.UUID]int{}
	for i, s := range out {
		ids[i], index[s.ID.UUID()] = s.ID.UUID(), i
	}
	rows, err = db.Query(ctx, sqlERPSnapshotLines, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read erp gl snapshot lines")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			sid             uuid.UUID
			l               erp.GLBalance
			debits, credits Numeric
		)
		if err := rows.Scan(&sid, &l.GLAccount, &debits, &credits); err != nil {
			return nil, mapError(err, "scan erp gl snapshot line")
		}
		i := index[sid]
		if l.Debits, err = debits.Money(out[i].Currency); err != nil {
			return nil, err
		}
		if l.Credits, err = credits.Money(out[i].Currency); err != nil {
			return nil, err
		}
		out[i].Lines = append(out[i].Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read erp gl snapshot lines")
	}
	for i := range out {
		slices.SortFunc(out[i].Lines, func(a, b erp.GLBalance) int { return strings.Compare(a.GLAccount, b.GLAccount) })
	}
	return out, nil
}

// RecordReconciliation writes a GL reconciliation and its items.
func (r *ERPRepo) RecordReconciliation(ctx context.Context, rec erp.GLReconciliation) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if rec.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The reconciliation belongs to a tenant outside the caller's scope.")
	}
	holds := make([]string, len(rec.Holds))
	for i, h := range rec.Holds {
		holds[i] = fmt.Sprintf("%s:%d", h.Reason, h.Journals)
	}
	var ranBy *uuid.UUID
	if !rec.RanBy.IsZero() {
		u := rec.RanBy.UUID()
		ranBy = &u
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlERPReconInsert, tenant.UUID(), rec.ID.UUID(), rec.Bridge.UUID(), rec.Snapshot.UUID(),
		rec.SnapshotDigest.String(), rec.LegalEntity.UUID(), rec.Period, string(rec.Currency), rec.MappingVersion,
		string(rec.Outcome), holds, rec.JournalCount, rec.PostedJournals, rec.PopulationDigest.String(), rec.RanAt.UTC(), ranBy); err != nil {
		return mapError(err, "insert erp gl reconciliation")
	}
	for i, it := range rec.Items {
		expected, err := optMoney(it.Expected)
		if err != nil {
			return err
		}
		observed, err := optMoney(it.Observed)
		if err != nil {
			return err
		}
		variance, err := optMoney(it.Variance)
		if err != nil {
			return err
		}
		accounts := make([]string, len(it.TCSLAccounts))
		for j, a := range it.TCSLAccounts {
			accounts[j] = string(a)
		}
		if _, err := db.Exec(ctx, sqlERPReconItemInsert, tenant.UUID(), rec.ID.UUID(), i+1, string(it.Key), optString(it.GLAccount),
			accounts, expected, observed, variance, string(it.Status), optString(it.Detail)); err != nil {
			return mapError(err, "insert erp gl reconciliation item")
		}
	}
	return nil
}

// Reconciliations returns a bridge's GL reconciliations, newest first.
func (r *ERPRepo) Reconciliations(ctx context.Context, bridge id.ERPBridgeID, period string) ([]erp.GLReconciliation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlERPReconList, tenant.UUID(), bridge.UUID(), period, erpListCap)
	if err != nil {
		return nil, mapError(err, "read erp gl reconciliations")
	}
	var out []erp.GLReconciliation
	for rows.Next() {
		var (
			rec                                   = erp.GLReconciliation{TenantID: tenant}
			rid, bridgeID, snapshot, entity       uuid.UUID
			ranBy                                 *uuid.UUID
			snapDigest, currency, outcome, popDig string
			holds                                 []string
		)
		if err := rows.Scan(&rid, &bridgeID, &snapshot, &snapDigest, &entity, &rec.Period, &currency, &rec.MappingVersion,
			&outcome, &holds, &rec.JournalCount, &rec.PostedJournals, &popDig, &rec.RanAt, &ranBy); err != nil {
			rows.Close()
			return nil, mapError(err, "scan erp gl reconciliation")
		}
		if rec.SnapshotDigest, err = canonical.ParseDigest(snapDigest); err == nil {
			rec.PopulationDigest, err = canonical.ParseDigest(popDig)
		}
		if err == nil {
			rec.Holds, err = parseHolds(holds)
		}
		if err != nil {
			rows.Close()
			return nil, erpFault(err, "A stored GL reconciliation does not read back.")
		}
		rec.ID, rec.Bridge, rec.Snapshot = id.NewGLReconciliationID(rid), id.NewERPBridgeID(bridgeID), id.NewGLSnapshotID(snapshot)
		rec.LegalEntity, rec.Currency, rec.Outcome, rec.RanAt = id.NewLegalEntityID(entity), fiscal.Currency(currency), erp.Outcome(outcome), rec.RanAt.UTC()
		if ranBy != nil {
			rec.RanBy = id.NewUserID(*ranBy)
		}
		out = append(out, rec)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read erp gl reconciliations")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	index := map[uuid.UUID]int{}
	for i, rec := range out {
		ids[i], index[rec.ID.UUID()] = rec.ID.UUID(), i
	}
	rows, err = db.Query(ctx, sqlERPReconItems, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read erp gl reconciliation items")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			rid                          uuid.UUID
			key, status                  string
			gl, detail                   *string
			accounts                     []string
			expected, observed, variance Numeric
		)
		if err := rows.Scan(&rid, &key, &gl, &accounts, &expected, &observed, &variance, &status, &detail); err != nil {
			return nil, mapError(err, "scan erp gl reconciliation item")
		}
		i := index[rid]
		cur := out[i].Currency
		it := erp.GLItem{Item: reconciliation.Item{Stage: reconciliation.R6SubledgerToGL, Key: reconciliation.MatchKey(key),
			Status: reconciliation.Status(status), Detail: deref(detail)}, GLAccount: deref(gl)}
		for _, pair := range []struct {
			n   Numeric
			dst **fiscal.Money
		}{{expected, &it.Expected}, {observed, &it.Observed}, {variance, &it.Variance}} {
			if !pair.n.Valid {
				continue
			}
			m, err := pair.n.Money(cur)
			if err != nil {
				return nil, err
			}
			*pair.dst = &m
		}
		for _, a := range accounts {
			it.TCSLAccounts = append(it.TCSLAccounts, subledger.Account(a))
		}
		out[i].Items = append(out[i].Items, it)
	}
	return out, mapError(rows.Err(), "read erp gl reconciliation items")
}

func parseHolds(stored []string) ([]erp.Hold, error) {
	var out []erp.Hold
	for _, s := range stored {
		reason, count, ok := strings.Cut(s, ":")
		if !ok {
			return nil, fmt.Errorf("postgres: hold %q is not REASON:count", s)
		}
		n, err := strconv.Atoi(count)
		if err != nil {
			return nil, fmt.Errorf("postgres: hold %q: %w", s, err)
		}
		out = append(out, erp.Hold{Reason: erp.HoldReason(reason), Journals: n})
	}
	return out, nil
}
