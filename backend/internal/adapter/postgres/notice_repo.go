package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/notice"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// regulatory_notice, regulatory_notice_event — migration 000030
// ---------------------------------------------------------------------------

const (
	sqlNoticeColumns = `notice_id, notice_type, authority, jurisdiction, legal_entity_id, entity_ambiguous,
		authority_reference, received_at, period_start, period_end, amount, currency, linked_case_id, links,
		created_at, created_by`
	sqlNoticeInsert = `INSERT INTO ztax.regulatory_notice (tenant_id, ` + sqlNoticeColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`
	sqlNoticeSelect = `SELECT ` + sqlNoticeColumns + ` FROM ztax.regulatory_notice WHERE tenant_id = $1`

	sqlNoticeEventColumns = `notice_id, seq, kind, status_after, note, evidence, authority_ref, deadline_kind, due_date,
		due_timezone, deadline_source, deadline_verified, authenticity, escalation_reason, legal_entity_id, owner_id,
		sla_due, attachment_sha256, attachment_name, attachment_media, response_version, response_sha256, outcome,
		outcome_amount, outcome_currency, settled, settlement, hearing_date, recorded_at, recorded_by`
	sqlNoticeEventInsert = `INSERT INTO ztax.regulatory_notice_event (tenant_id, ` + sqlNoticeEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
		$22, $23, $24, $25, $26, $27, $28, $29, $30, $31)`
	sqlNoticeEventSelect = `SELECT ` + sqlNoticeEventColumns + ` FROM ztax.regulatory_notice_event WHERE tenant_id = $1`

	// noticeListCap bounds a list read.
	noticeListCap = 500
)

// NoticeRepo implements port.NoticeRepository.
type NoticeRepo struct{ s *Store }

// Notices returns the regulatory notice repository.
func (s *Store) Notices() *NoticeRepo { return &NoticeRepo{s: s} }

var _ port.NoticeRepository = (*NoticeRepo)(nil)

type noticeLink struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
}

// Lock takes one notice's lock for the transaction.
func (r *NoticeRepo) Lock(ctx context.Context, noticeID id.NoticeID) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlPackageLock, tenant.String()+"/notice/"+noticeID.String())
	return mapError(err, "lock regulatory notice")
}

// Create records a notice and its first events.
func (r *NoticeRepo) Create(ctx context.Context, n notice.Notice, events []notice.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if n.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The notice belongs to a tenant outside the caller's scope.")
	}
	if err := n.Validate(); err != nil {
		return obligationFault(err, "The regulatory notice is not well-formed.")
	}
	if len(events) == 0 || events[0].Kind != notice.EventReceived || events[0].Seq != 1 {
		return obligationFault(nil, "A regulatory notice is created by its RECEIVED event.")
	}
	links := make([]noticeLink, len(n.Links))
	for i, l := range n.Links {
		links[i] = noticeLink{Kind: string(l.Kind), Reference: l.Reference}
	}
	rawLinks, err := json.Marshal(links)
	if err != nil {
		return obligationFault(err, "The notice's links could not be encoded.")
	}
	var (
		amount   any
		currency *string
	)
	if n.Amount != nil {
		v, err := MoneyValue(*n.Amount)
		if err != nil {
			return err
		}
		amount = v
		c := string(n.Amount.Currency())
		currency = &c
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlNoticeInsert, tenant.UUID(), n.ID.UUID(), string(n.Type), n.Authority, n.Jurisdiction,
		optLegalEntity(n.LegalEntity), n.EntityAmbiguous, n.AuthorityRef, n.ReceivedAt.UTC(), optDate(n.PeriodStart),
		optDate(n.PeriodEnd), amount, currency, optString(n.LinkedCase), rawLinks, n.CreatedAt.UTC(), n.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert regulatory notice")
	}
	return r.AppendEvents(ctx, n.ID, events)
}

// AppendEvents records events in order.
func (r *NoticeRepo) AppendEvents(ctx context.Context, noticeID id.NoticeID, events []notice.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	for _, e := range events {
		var (
			deadlineKind, tz, source *string
			due                      pgtype.Date
			verified                 *bool
			attSHA, attName, attMed  *string
			version                  *int
			outAmount                any
			outCurrency              *string
			settled                  *bool
			hearing                  pgtype.Date
		)
		if d := e.Deadline; d != nil {
			k, z, s, v := string(d.Kind), d.Timezone, string(d.Source), d.Verified
			deadlineKind, tz, source, verified = &k, &z, &s, &v
			due = optDate(d.Date)
		}
		if a := e.Attachment; a != nil {
			attSHA, attName, attMed = &a.SHA256, &a.Filename, &a.MediaType
		}
		if e.ResponseVersion > 0 {
			v := e.ResponseVersion
			version = &v
		}
		if e.OutcomeAmount != nil {
			v, err := MoneyValue(*e.OutcomeAmount)
			if err != nil {
				return err
			}
			outAmount = v
			c := string(e.OutcomeAmount.Currency())
			outCurrency = &c
		}
		if e.Kind == notice.EventOutcome {
			s := e.Settled
			settled = &s
		}
		hearing = optDate(e.HearingDate)
		var authenticity *string
		if e.Kind == notice.EventAuthenticity {
			authenticity = optString(string(e.Authenticity))
		}
		if _, err := r.s.db(ctx).Exec(ctx, sqlNoticeEventInsert, tenant.UUID(), noticeID.UUID(), e.Seq, string(e.Kind),
			string(e.Status), optString(e.Note), optString(e.Evidence), optString(e.AuthorityRef), deadlineKind, due, tz,
			source, verified, authenticity, optString(string(e.Escalation)), optLegalEntity(e.LegalEntity), optUser(e.Owner),
			optTimestamp(e.SLADue), attSHA, attName, attMed, version, optString(e.ResponseSHA256), optString(string(e.Outcome)),
			outAmount, outCurrency, settled, optString(string(e.Settlement)), hearing, e.RecordedAt.UTC(),
			e.RecordedBy.UUID()); err != nil {
			return mapError(err, "insert regulatory notice event")
		}
	}
	return nil
}

// Notice returns one notice.
func (r *NoticeRepo) Notice(ctx context.Context, noticeID id.NoticeID) (notice.Notice, error) {
	got, err := r.read(ctx, ` AND notice_id = $2`, noticeID.UUID())
	if err != nil {
		return notice.Notice{}, err
	}
	if len(got) == 0 {
		return notice.Notice{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such regulatory notice.")
	}
	return got[0], nil
}

// List returns the tenant's notices, most recently received first.
func (r *NoticeRepo) List(ctx context.Context, limit int) ([]notice.Notice, error) {
	if limit <= 0 || limit > noticeListCap {
		limit = noticeListCap
	}
	return r.read(ctx, ` AND notice_id IN (SELECT notice_id FROM ztax.regulatory_notice WHERE tenant_id = $1
		ORDER BY received_at DESC, notice_id DESC LIMIT $2)`, limit)
}

// read loads the notices a predicate selects and folds each one's events. The
// predicate is a fixed string from this file, never caller text.
func (r *NoticeRepo) read(ctx context.Context, where string, arg any) ([]notice.Notice, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)

	rows, err := db.Query(ctx, sqlNoticeSelect+where+` ORDER BY received_at DESC, notice_id DESC`, tenant.UUID(), arg)
	if err != nil {
		return nil, mapError(err, "read regulatory notices")
	}
	var out []notice.Notice
	for rows.Next() {
		var (
			n          = notice.Notice{TenantID: tenant}
			nid, by    uuid.UUID
			le         *uuid.UUID
			kind       string
			start, end pgtype.Date
			amount     Numeric
			currency   *string
			linkedCase *string
			rawLinks   []byte
		)
		if err := rows.Scan(&nid, &kind, &n.Authority, &n.Jurisdiction, &le, &n.EntityAmbiguous, &n.AuthorityRef,
			&n.ReceivedAt, &start, &end, &amount, &currency, &linkedCase, &rawLinks, &n.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan regulatory notice")
		}
		n.ID, n.Type, n.CreatedBy = id.NewNoticeID(nid), notice.Type(kind), id.NewUserID(by)
		if le != nil {
			n.LegalEntity = id.NewLegalEntityID(*le)
		}
		n.ReceivedAt, n.CreatedAt = n.ReceivedAt.UTC(), n.CreatedAt.UTC()
		if start.Valid {
			n.PeriodStart, n.PeriodEnd = inLocation(start, time.UTC), inLocation(end, time.UTC)
		}
		if amount.Valid {
			m, err := amount.Money(fiscal.Currency(deref(currency)))
			if err != nil {
				rows.Close()
				return nil, err
			}
			n.Amount = &m
		}
		n.LinkedCase = deref(linkedCase)
		var links []noticeLink
		if err := json.Unmarshal(rawLinks, &links); err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored regulatory notice carries links that do not decode.")
		}
		for _, l := range links {
			n.Links = append(n.Links, notice.Link{Kind: notice.LinkKind(l.Kind), Reference: l.Reference})
		}
		out = append(out, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read regulatory notices")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, n := range out {
		ids[i] = n.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlNoticeEventSelect+` AND notice_id = ANY($2) ORDER BY notice_id, seq`, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read regulatory notice events")
	}
	defer rows.Close()
	events := map[id.NoticeID][]notice.Event{}
	for rows.Next() {
		var (
			e                                              notice.Event
			nid, by                                        uuid.UUID
			kind, status                                   string
			note, evidence, ref, dKind, tz, source         *string
			authenticity, escalation, attSHA, attName      *string
			attMedia, respSHA, outcome, outCur, settlement *string
			due, hearing                                   pgtype.Date
			verified, settled                              *bool
			le, owner                                      *uuid.UUID
			sla                                            *time.Time
			version                                        *int
			outAmount                                      Numeric
		)
		if err := rows.Scan(&nid, &e.Seq, &kind, &status, &note, &evidence, &ref, &dKind, &due, &tz, &source, &verified,
			&authenticity, &escalation, &le, &owner, &sla, &attSHA, &attName, &attMedia, &version, &respSHA, &outcome,
			&outAmount, &outCur, &settled, &settlement, &hearing, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan regulatory notice event")
		}
		e.Kind, e.Status, e.RecordedAt, e.RecordedBy = notice.EventKind(kind), notice.Status(status), e.RecordedAt.UTC(), id.NewUserID(by)
		e.Note, e.Evidence, e.AuthorityRef = deref(note), deref(evidence), deref(ref)
		if dKind != nil {
			d := notice.Deadline{Kind: notice.DeadlineKind(*dKind), Timezone: deref(tz), Source: notice.DeadlineSource(deref(source))}
			if verified != nil {
				d.Verified = *verified
			}
			if due.Valid {
				d.Date = inLocation(due, time.UTC)
			}
			e.Deadline = &d
		}
		e.Authenticity, e.Escalation = notice.Authenticity(deref(authenticity)), notice.EscalationReason(deref(escalation))
		if le != nil {
			e.LegalEntity = id.NewLegalEntityID(*le)
		}
		if owner != nil {
			e.Owner = id.NewUserID(*owner)
		}
		if sla != nil {
			e.SLADue = sla.UTC()
		}
		if attSHA != nil {
			e.Attachment = &notice.Attachment{SHA256: *attSHA, Filename: deref(attName), MediaType: deref(attMedia)}
		}
		if version != nil {
			e.ResponseVersion = *version
		}
		e.ResponseSHA256, e.Outcome = deref(respSHA), notice.Outcome(deref(outcome))
		if outAmount.Valid {
			m, err := outAmount.Money(fiscal.Currency(deref(outCur)))
			if err != nil {
				return nil, err
			}
			e.OutcomeAmount = &m
		}
		if settled != nil {
			e.Settled = *settled
		}
		e.Settlement = notice.Settlement(deref(settlement))
		if hearing.Valid {
			e.HearingDate = inLocation(hearing, time.UTC)
		}
		k := id.NewNoticeID(nid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read regulatory notice events")
	}
	for i := range out {
		folded, err := notice.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored regulatory notice's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

func optLegalEntity(le id.LegalEntityID) *uuid.UUID {
	if le.IsZero() {
		return nil
	}
	u := le.UUID()
	return &u
}

// optDate is a nullable civil date column.
func optDate(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return civilDate(t)
}
