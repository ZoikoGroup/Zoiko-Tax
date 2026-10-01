package content

import (
	"strings"
	"time"
)

// Role is the capacity in which a principal approved a content artifact
// (CONT-001 §8 "Approvals: author, independent reviewer, legal/content
// approver"; §30's RACI).
type Role string

// The roles.
const (
	// RoleAuthor wrote the content. Recording authorship as an approval — the
	// author attests "this is what I wrote" over the exact digest — is what
	// lets the four-eyes rule compare the author with the approver at all.
	RoleAuthor Role = "AUTHOR"
	// RoleReviewer reviewed it independently. Optional at the four-eyes
	// minimum; ZTAX-CONT-REQ-0058 wants one named in release evidence.
	RoleReviewer Role = "REVIEWER"
	// RoleApprover authorised it for release.
	RoleApprover Role = "APPROVER"
	// RoleSeniorApprover is the designated senior content or legal approver a
	// high-impact or ambiguous change additionally needs (ZTAX-CONT-REQ-0014).
	RoleSeniorApprover Role = "SENIOR_APPROVER"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleAuthor, RoleReviewer, RoleApprover, RoleSeniorApprover:
		return true
	}
	return false
}

// Approval is one principal's signed attestation over one artifact, as the
// four-eyes rule sees it. The signature itself is the bundle format's concern
// (internal/content/bundle); by the time an Approval reaches this package the
// signature has verified and KeyID is the key it verified against.
type Approval struct {
	Role      Role
	Principal string
	KeyID     string
	At        time.Time
}

// nonHumanKinds are principal kinds that may never approve content. An AI
// principal may draft and propose (CONT-001 §7) but "no AI principal may
// transition a ContentChange or RuleDefinition to APPROVED/PRODUCTION"
// (ZTAX-CONT-REQ-0011, -0012). Service and system principals are refused for
// the same reason: an approval is a person taking responsibility, and a
// process cannot.
var nonHumanKinds = map[string]bool{
	"ai": true, "agent": true, "model": true, "bot": true, "service": true, "system": true, "workload": true,
}

// ValidatePrincipal checks a principal is named as kind:identifier, the shape
// CONT-001 §36.2 uses ("analyst:2048", "counsel:113"), and is not a non-human
// kind.
//
// This is a W1 approximation of a principal model the identity domain does not
// yet carry for workforce identities: it can refuse a principal that *says*
// it is an AI, and it cannot detect one that lies. The control that closes that
// gap is that an approval is signed by a key issued to a person; the kind
// check is the cheap second line that makes an honest mistake impossible.
func ValidatePrincipal(p string) error {
	kind, id, ok := strings.Cut(p, ":")
	if !ok || kind == "" || id == "" {
		return errorf("principal %q must be kind:identifier", p)
	}
	if kind != strings.ToLower(kind) {
		return errorf("principal %q: the kind is lower-case", p)
	}
	if nonHumanKinds[kind] {
		return errorf("principal %q is a %s principal; only a person approves content (ZTAX-CONT-REQ-0012)", p, kind)
	}
	if strings.ContainsAny(p, " \t\n") {
		return errorf("principal %q contains whitespace", p)
	}
	return nil
}

// CheckFourEyes enforces CONT-001 §8's four-eyes minimum over a set of
// approvals: "no material production content change may be approved by the
// same person who authored it" (ZTAX-CONT-REQ-0013, -0046).
//
// Concretely, all of:
//
//   - at least one AUTHOR and at least one APPROVER;
//   - every principal is a person (ValidatePrincipal) and appears once — one
//     person holding two roles on one artifact is exactly the self-approval the
//     rule forbids, whichever two roles they are;
//   - every approval was signed by a different key, so that two principals
//     cannot be one key-holder writing two names;
//   - no approval was signed by releaseKeyID, the key that seals the release.
//     Content author, content approver and release signer are distinct
//     privileges (CONT-001 §27, ZTAX-CONT-REQ-0047); a release signer that
//     could also approve would make the seal its own second pair of eyes.
//     An empty releaseKeyID skips that check, for artifacts that are approved
//     but not sealed (an Interpretation).
func CheckFourEyes(approvals []Approval, releaseKeyID string) error {
	principals := map[string]Role{}
	keys := map[string]string{}
	var author, approver bool
	for _, a := range approvals {
		if !a.Role.Valid() {
			return errorf("approval by %q has role %q", a.Principal, a.Role)
		}
		if err := ValidatePrincipal(a.Principal); err != nil {
			return err
		}
		if a.KeyID == "" || a.At.IsZero() {
			return errorf("approval by %s names no key or no instant", a.Principal)
		}
		if prior, dup := principals[a.Principal]; dup {
			return errorf("%s approves as both %s and %s; four-eyes needs a second person (ZTAX-CONT-REQ-0013)", a.Principal, prior, a.Role)
		}
		principals[a.Principal] = a.Role
		if other, dup := keys[a.KeyID]; dup {
			return errorf("%s and %s approved with the same key %s; one key is one pair of eyes", other, a.Principal, a.KeyID)
		}
		keys[a.KeyID] = a.Principal
		if releaseKeyID != "" && a.KeyID == releaseKeyID {
			return errorf("%s approved with the release signing key %s; approving and signing a release are separate privileges (ZTAX-CONT-REQ-0047)", a.Principal, a.KeyID)
		}
		switch a.Role {
		case RoleAuthor:
			author = true
		case RoleApprover, RoleSeniorApprover:
			approver = true
		}
	}
	switch {
	case !author && !approver:
		return errorf("no approvals; four-eyes needs an AUTHOR and an APPROVER (ZTAX-CONT-REQ-0013)")
	case !author:
		return errorf("no AUTHOR approval; four-eyes compares the approver with the author, and there is no author to compare")
	case !approver:
		return errorf("no APPROVER approval; the author cannot be the only pair of eyes (ZTAX-CONT-REQ-0013)")
	}
	return nil
}

func hasRole(approvals []Approval, r Role) bool {
	for _, a := range approvals {
		if a.Role == r {
			return true
		}
	}
	return false
}
