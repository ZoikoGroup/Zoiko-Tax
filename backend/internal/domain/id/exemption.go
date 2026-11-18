package id

import "github.com/google/uuid"

// ExemptionCertificateID identifies one exemption certificate held as
// evidence (Engineering Doc 17, G-048): a buyer's claimed exemption, as
// received, with every replacement document a new version of it. It is kept
// in its own file so the evidence register adds a type without touching the
// shared list in id.go.
type ExemptionCertificateID struct{ base }

// NewExemptionCertificateID wraps a raw UUID as an ExemptionCertificateID.
func NewExemptionCertificateID(u uuid.UUID) ExemptionCertificateID {
	return ExemptionCertificateID{base{u}}
}

// ParseExemptionCertificateID reads an ExemptionCertificateID from its
// canonical string form.
func ParseExemptionCertificateID(s string) (ExemptionCertificateID, error) {
	u, err := parse("exemption certificate id", s)
	if err != nil {
		return ExemptionCertificateID{}, err
	}
	return NewExemptionCertificateID(u), nil
}
