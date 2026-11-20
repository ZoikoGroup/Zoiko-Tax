package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// ExemptionCertificateID returns a fresh ExemptionCertificateID.
func ExemptionCertificateID(g Generator) (id.ExemptionCertificateID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.ExemptionCertificateID{}, err
	}
	return id.NewExemptionCertificateID(u), nil
}
