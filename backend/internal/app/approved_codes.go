package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"
)

// ApprovedCodes says whether an ontology code is approved at a given time.
type ApprovedCodes interface {
	Approved(code string, at time.Time) bool
}

// ErrApprovedListInvalid is the only error LoadApprovedList returns. The
// message is fixed: it never names the file, an entry or a value.
var ErrApprovedListInvalid = errors.New("approved ontology list: invalid or unreadable")

// The file format is the one the Python side reads
// (intelligence/src/ztax_gateway/ontology_guard.py):
//
//	{"list_version": "...", "approved": [{"code", "effective_from", "effective_to"?}]}
//
// Dates are YYYY-MM-DD and the window is inclusive on both ends.
const maxApprovedListBytes = 1 << 20

var (
	approvedCodeRE = regexp.MustCompile(`^ontology:[A-Za-z0-9_./:-]+$`)
	approvedDateRE = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// approvedWindow holds ISO dates, which compare correctly as strings. An empty
// `to` is open-ended.
type approvedWindow struct{ from, to string }

// ApprovedList is an immutable approved-ontology list.
type ApprovedList struct{ byCode map[string]approvedWindow }

type approvedFile struct {
	ListVersion *string `json:"list_version"`
	Approved    []struct {
		Code          *string `json:"code"`
		EffectiveFrom *string `json:"effective_from"`
		EffectiveTo   *string `json:"effective_to"`
	} `json:"approved"`
}

func validDate(s string) bool {
	if !approvedDateRE.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// ParseApprovedList parses and validates a list. Any problem is
// ErrApprovedListInvalid.
func ParseApprovedList(b []byte) (*ApprovedList, error) {
	if len(b) > maxApprovedListBytes {
		return nil, ErrApprovedListInvalid
	}
	var f approvedFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil || dec.More() {
		return nil, ErrApprovedListInvalid
	}
	if f.ListVersion == nil || strings.TrimSpace(*f.ListVersion) == "" || len(f.Approved) == 0 {
		return nil, ErrApprovedListInvalid
	}
	l := &ApprovedList{byCode: make(map[string]approvedWindow, len(f.Approved))}
	for _, e := range f.Approved {
		if e.Code == nil || e.EffectiveFrom == nil || !approvedCodeRE.MatchString(*e.Code) || !validDate(*e.EffectiveFrom) {
			return nil, ErrApprovedListInvalid
		}
		w := approvedWindow{from: *e.EffectiveFrom}
		if e.EffectiveTo != nil {
			if !validDate(*e.EffectiveTo) || *e.EffectiveTo < w.from {
				return nil, ErrApprovedListInvalid
			}
			w.to = *e.EffectiveTo
		}
		if _, dup := l.byCode[*e.Code]; dup {
			return nil, ErrApprovedListInvalid
		}
		l.byCode[*e.Code] = w
	}
	return l, nil
}

// LoadApprovedList reads and parses the file at path.
func LoadApprovedList(path string) (*ApprovedList, error) {
	// #nosec G304 -- a path from this process's own configuration.
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrApprovedListInvalid
	}
	return ParseApprovedList(b)
}

// Approved is an exact, case-sensitive match on the code (no trimming). The
// UTC date of `at` must fall inside the window, inclusive on both ends.
func (l *ApprovedList) Approved(code string, at time.Time) bool {
	w, ok := l.byCode[code]
	if !ok {
		return false
	}
	day := at.UTC().Format("2006-01-02")
	return day >= w.from && (w.to == "" || day <= w.to)
}
