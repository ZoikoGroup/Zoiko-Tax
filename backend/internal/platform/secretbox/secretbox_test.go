package secretbox_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/secretbox"
)

func key(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, secretbox.KeyBytes))
}

func TestSealedValuesOpenOnlyUnderTheirKeyAndBinding(t *testing.T) {
	box, err := secretbox.New(key(1))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte("signing secret"), []byte("tenant/webhook/1"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Open(sealed, []byte("tenant/webhook/1"))
	if err != nil || string(plain) != "signing secret" {
		t.Fatalf("open: %q %v", plain, err)
	}

	again, _ := box.Seal([]byte("signing secret"), []byte("tenant/webhook/1"))
	if bytes.Equal(sealed, again) {
		t.Fatal("two seals of one secret are identical; the nonce is not random")
	}

	other, _ := secretbox.New(key(2))
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	for name, open := range map[string]func() ([]byte, error){
		"another binding": func() ([]byte, error) { return box.Open(sealed, []byte("tenant/webhook/2")) },
		"another key":     func() ([]byte, error) { return other.Open(sealed, []byte("tenant/webhook/1")) },
		"tampered":        func() ([]byte, error) { return box.Open(tampered, []byte("tenant/webhook/1")) },
		"truncated":       func() ([]byte, error) { return box.Open(sealed[:5], []byte("tenant/webhook/1")) },
		"unknown format":  func() ([]byte, error) { return box.Open(append([]byte{9}, sealed[1:]...), []byte("tenant/webhook/1")) },
	} {
		if _, err := open(); !errors.Is(err, secretbox.ErrOpen) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestKeyMustBe32Bytes(t *testing.T) {
	for _, k := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := secretbox.New(k); err == nil {
			t.Errorf("%q was accepted as a key", k)
		}
	}
}
