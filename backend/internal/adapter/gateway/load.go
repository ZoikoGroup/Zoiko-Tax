package gateway

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// policyFile is the reviewed AI-train registry. The Python Gateway loads the
// same file (ztax_gateway.server.load_config), so the Go pre-check and the
// Gateway's own decision are made against one registry. The route and
// description are the Gateway's and are read here only to be refused if
// absent; the Go side has no use for routing.
type policyFile struct {
	UseCases []struct {
		UseCaseID            string          `json:"use_case_id"`
		Owner                string          `json:"owner"`
		Description          string          `json:"description"`
		MaxRiskTier          string          `json:"max_risk_tier"`
		MaxAuthority         string          `json:"max_authority"`
		PermittedRegions     []string        `json:"permitted_regions"`
		PermittedDataClasses []string        `json:"permitted_data_classes"`
		Suspended            bool            `json:"suspended"`
		Route                json.RawMessage `json:"route"`
	} `json:"use_cases"`
	Killed     []string `json:"killed"`
	GlobalKill bool     `json:"global_kill"`
}

// LoadPolicy reads the registry file into a validated policy snapshot.
func LoadPolicy(path string) (ai.Policy, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ai.Policy{}, fmt.Errorf("gateway: read policy: %w", err)
	}
	var f policyFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return ai.Policy{}, fmt.Errorf("gateway: parse policy %s: %w", path, err)
	}
	var ucs []ai.UseCasePolicy
	for _, u := range f.UseCases {
		if len(u.Route) == 0 {
			return ai.Policy{}, fmt.Errorf("gateway: use case %s has no route; the Gateway could not serve it", u.UseCaseID)
		}
		classes := make([]privacy.Class, len(u.PermittedDataClasses))
		for i, c := range u.PermittedDataClasses {
			classes[i] = privacy.Class(c)
		}
		ucs = append(ucs, ai.UseCasePolicy{
			UseCase: ai.AiUseCase(u.UseCaseID), Owner: u.Owner,
			MaxAuthority: ai.AuthorityOutcome(u.MaxAuthority), MaxRiskTier: ai.RiskTier(u.MaxRiskTier),
			PermittedRegions: u.PermittedRegions, PermittedDataClasses: classes, Suspended: u.Suspended,
		})
	}
	killed := make([]ai.AiUseCase, len(f.Killed))
	for i, k := range f.Killed {
		killed[i] = ai.AiUseCase(k)
	}
	return ai.NewPolicy(ucs, killed, f.GlobalKill)
}

// LoadMTLS reads a workload's mTLS material from a mounted secret directory:
// tls.crt and tls.key are the certificate this workload presents, ca.crt the
// authority the Gateway's certificate must chain to.
func LoadMTLS(dir string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	if err != nil {
		return nil, fmt.Errorf("gateway: client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(filepath.Clean(dir), "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("gateway: gateway CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("gateway: ca.crt holds no certificate")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
