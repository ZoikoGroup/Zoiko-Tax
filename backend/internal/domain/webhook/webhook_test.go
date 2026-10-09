package webhook_test

import (
	"encoding/base64"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/webhook"
)

func TestEndpointMustBeHTTPSWithoutCredentialsOrPrivateLiterals(t *testing.T) {
	ok := map[string]string{
		"https://hooks.example.com/zt":       "https://hooks.example.com/zt",
		"https://Hooks.Example.COM:8443/a?b": "https://hooks.example.com:8443/a?b",
		"https://93.184.216.34/in":           "https://93.184.216.34/in",
	}
	for in, want := range ok {
		got, err := webhook.ValidateEndpoint(in, false)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "not a url", "http://hooks.example.com/", "ftp://hooks.example.com/",
		"https://user:pass@hooks.example.com/", "https://hooks.example.com/#frag",
		"https://127.0.0.1/", "https://10.1.2.3/", "https://169.254.169.254/latest/meta-data",
		"https://[::1]/", "https://[fd00::1]/", "https://[::ffff:127.0.0.1]/", "https://localhost/",
		"https://api.localhost/", "https:///nohost", "https://hooks.example.com:0/", "https://hooks.example.com:99999/",
		"https://hooks.example.com/" + strings.Repeat("a", webhook.MaxURLLength),
	} {
		if got, err := webhook.ValidateEndpoint(bad, false); err == nil {
			t.Errorf("%q validated as %q", bad, got)
		}
	}
	// Development may point a webhook at a listener on the laptop.
	if _, err := webhook.ValidateEndpoint("http://localhost:9000/hook", true); err != nil {
		t.Errorf("development: %v", err)
	}
}

func TestEgressPolicyForbidsTheCellsOwnNetwork(t *testing.T) {
	for _, s := range []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1", "172.31.255.255",
		"192.168.1.1", "224.0.0.1", "255.255.255.255", "::", "::1", "::ffff:10.0.0.1", "64:ff9b::a00:1",
		"fc00::1", "fd12:3456::1", "fe80::1", "ff02::1",
	} {
		if !webhook.Forbidden(netip.MustParseAddr(s)) {
			t.Errorf("%s is permitted", s)
		}
	}
	for _, s := range []string{"93.184.216.34", "8.8.8.8", "172.32.0.1", "2606:4700::6810:85e5"} {
		if webhook.Forbidden(netip.MustParseAddr(s)) {
			t.Errorf("%s is forbidden", s)
		}
	}
	if !webhook.Forbidden(netip.Addr{}) {
		t.Error("the zero address is permitted")
	}
}

func TestSignatureIsStandardWebhooks(t *testing.T) {
	// The Standard Webhooks specification's worked example. Its secret is
	// 24 bytes, so it is decoded here rather than through DecodeSecret, which
	// holds this cell's own secrets to 32.
	specSecret, err := base64.StdEncoding.DecodeString("MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	if err != nil {
		t.Fatal(err)
	}
	got := webhook.Sign(specSecret, "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; got != want {
		t.Fatalf("the specification's example signs as %s, want %s", got, want)
	}
	if _, err := webhook.DecodeSecret("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"); err == nil {
		t.Fatal("a 24-byte secret decoded as one of this cell's")
	}

	raw := make([]byte, webhook.SecretBytes)
	for i := range raw {
		raw[i] = byte(i)
	}
	encoded := webhook.EncodeSecret(raw)
	back, err := webhook.DecodeSecret(encoded)
	if err != nil || string(back) != string(raw) {
		t.Fatalf("round trip: %v", err)
	}

	at := time.Unix(1614265330, 0)
	body := []byte(`{"test": 2432232314}`)
	sig := webhook.Sign(raw, "msg_p5jXN8AQM9LWM0D4loKWxJek", at, body)
	if !strings.HasPrefix(sig, "v1,") {
		t.Fatalf("signature %q", sig)
	}
	if !webhook.Verify(raw, "msg_p5jXN8AQM9LWM0D4loKWxJek", at, body, "v1,bogus "+sig) {
		t.Fatal("a valid signature among several did not verify")
	}
	for name, tamper := range map[string]func() bool{
		"body": func() bool {
			return webhook.Verify(raw, "msg_p5jXN8AQM9LWM0D4loKWxJek", at, []byte(`{"test": 1}`), sig)
		},
		"id": func() bool { return webhook.Verify(raw, "msg_other", at, body, sig) },
		"time": func() bool {
			return webhook.Verify(raw, "msg_p5jXN8AQM9LWM0D4loKWxJek", at.Add(time.Second), body, sig)
		},
	} {
		if tamper() {
			t.Errorf("a tampered %s verified", name)
		}
	}
}

func TestRotationSignsWithBothSecretsDuringTheOverlap(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	versions := []webhook.SecretVersion{
		{Version: 1, CreatedAt: t0},
		{Version: 2, CreatedAt: t0.Add(time.Hour), RetiresPreviousAt: t0.Add(time.Hour + webhook.RotationOverlap)},
	}
	if got := webhook.Signing(versions, t0.Add(2*time.Hour)); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("during the overlap: %v", got)
	}
	if got := webhook.Signing(versions, t0.Add(time.Hour+webhook.RotationOverlap)); len(got) != 1 || got[0] != 2 {
		t.Fatalf("after the overlap: %v", got)
	}
	// Two rotations in a row: version 1 is gone, whatever its overlap said.
	versions = append(versions, webhook.SecretVersion{Version: 3, RetiresPreviousAt: t0.Add(3 * time.Hour)})
	if got := webhook.Signing(versions, t0.Add(2*time.Hour)); len(got) != 2 || got[0] != 3 || got[1] != 2 {
		t.Fatalf("after a second rotation: %v", got)
	}
}

func TestBackoffDeadLettersAfterTheLastAttempt(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var total time.Duration
	for n := 1; n < webhook.MaxAttempts; n++ {
		status, next := webhook.AfterFailure(n, at)
		if status != webhook.DeliveryPending || !next.After(at) {
			t.Fatalf("attempt %d: %s %s", n, status, next)
		}
		total += next.Sub(at)
	}
	if status, _ := webhook.AfterFailure(webhook.MaxAttempts, at); status != webhook.DeliveryDead {
		t.Fatalf("the last attempt left the delivery %s", status)
	}
	if total < 6*time.Hour || total > 48*time.Hour {
		t.Fatalf("retries span %s", total)
	}
	for status, want := range map[int]bool{200: true, 204: true, 299: true, 301: false, 400: false, 410: false, 500: false} {
		if webhook.Succeeded(status) != want {
			t.Errorf("%d succeeded=%v", status, !want)
		}
	}
}

func TestSubscriptionsNameTheirEventsExplicitly(t *testing.T) {
	known := []string{"com.zoikotax.a", "com.zoikotax.b"}
	if _, err := webhook.NormaliseEventTypes(nil, known); err == nil {
		t.Error("an empty set was accepted")
	}
	if _, err := webhook.NormaliseEventTypes([]string{"*"}, known); err == nil {
		t.Error("a wildcard was accepted")
	}
	got, err := webhook.NormaliseEventTypes([]string{"com.zoikotax.b", "com.zoikotax.a", "com.zoikotax.b"}, known)
	if err != nil || len(got) != 2 || got[0] != "com.zoikotax.a" {
		t.Fatalf("%v %v", got, err)
	}
	s := webhook.Subscription{EventTypes: got}
	if !s.Wants("com.zoikotax.a") || s.Wants("com.zoikotax.c") {
		t.Error("Wants")
	}
}

func TestStatusLifecycle(t *testing.T) {
	cases := []struct {
		from, to webhook.Status
		ok       bool
	}{
		{webhook.StatusActive, webhook.StatusPaused, true},
		{webhook.StatusPaused, webhook.StatusActive, true},
		{webhook.StatusActive, webhook.StatusDisabled, true},
		{webhook.StatusDisabled, webhook.StatusActive, false},
		{webhook.StatusActive, webhook.StatusActive, false},
	}
	for _, c := range cases {
		if c.from.CanMoveTo(c.to) != c.ok {
			t.Errorf("%s -> %s", c.from, c.to)
		}
	}
}
