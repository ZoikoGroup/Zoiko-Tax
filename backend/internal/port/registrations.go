package port

import (
	"context"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
)

// RegistrationRepository stores the registration ledger and the exemption
// certificates held as evidence (Engineering Doc 17). It is append-only: a
// record row is never updated or deleted, and its state is the fold of its
// events.
type RegistrationRepository interface {
	// Lock serializes the tenant's writes to one key for the rest of the
	// transaction: a record, an intake key or a document digest.
	Lock(ctx context.Context, key string) error

	// CreateRegistration writes a registration and its first events.
	CreateRegistration(ctx context.Context, r registration.Registration, events []registration.Event) error
	// AppendRegistrationEvents writes a registration's next events.
	AppendRegistrationEvents(ctx context.Context, registrationID id.RegistrationID, events []registration.Event) error
	// Registration returns one registration, folded.
	Registration(ctx context.Context, registrationID id.RegistrationID) (registration.Registration, error)
	// RegistrationByIntakeKey returns the registration created under an
	// Idempotency-Key, and false when there is none.
	RegistrationByIntakeKey(ctx context.Context, key string) (registration.Registration, bool, error)
	// RegistrationsByNumber returns every registration of a scheme and
	// number, for any of the tenant's legal entities.
	RegistrationsByNumber(ctx context.Context, scheme registration.Scheme, number string) ([]registration.Registration, error)
	// Registrations returns the tenant's most recent registrations, newest
	// first, at most limit.
	Registrations(ctx context.Context, limit int) ([]registration.Registration, error)

	// CreateCertificate writes a certificate and its RECEIVED event.
	CreateCertificate(ctx context.Context, c registration.Certificate, received registration.CertificateEvent) error
	// AppendCertificateEvent writes a certificate's next event.
	AppendCertificateEvent(ctx context.Context, certificateID id.ExemptionCertificateID, e registration.CertificateEvent) error
	// Certificate returns one certificate, folded.
	Certificate(ctx context.Context, certificateID id.ExemptionCertificateID) (registration.Certificate, error)
	// CertificateByIntakeKey returns the certificate received under an
	// Idempotency-Key, and false when there is none.
	CertificateByIntakeKey(ctx context.Context, key string) (registration.Certificate, bool, error)
	// CertificatesWithDocument returns every certificate any of whose
	// versions recorded a document with this digest.
	CertificatesWithDocument(ctx context.Context, sha256 string) ([]registration.Certificate, error)
	// Certificates returns the tenant's most recent certificates, newest
	// first, at most limit.
	Certificates(ctx context.Context, limit int) ([]registration.Certificate, error)
}
