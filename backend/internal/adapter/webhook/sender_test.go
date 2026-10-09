package webhook_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	adapter "github.com/zoikogroup/zoikotax/backend/internal/adapter/webhook"
)

func TestTheGuardRefusesALoopbackReceiver(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true }))
	defer srv.Close()

	_, err := adapter.NewSender(false).Send(context.Background(), srv.URL, nil, []byte(`{}`))
	if !errors.Is(err, adapter.ErrForbiddenDestination) {
		t.Fatalf("a loopback receiver: %v", err)
	}
	if hit {
		t.Fatal("the request reached the loopback receiver")
	}
}

func TestDeliveryCarriesItsHeadersAndFollowsNoRedirect(t *testing.T) {
	var got http.Header
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/elsewhere" {
			t.Error("a redirect was followed")
		}
		got = r.Header.Clone()
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := adapter.NewSender(true) // development: the receiver is on loopback
	status, err := s.Send(context.Background(), srv.URL+"/hook", map[string]string{"webhook-id": "e1"}, []byte(`{"a":1}`))
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("send: %d %v", status, err)
	}
	if got.Get("webhook-id") != "e1" || got.Get("Content-Type") != "application/cloudevents+json" || string(body) != `{"a":1}` {
		t.Fatalf("headers %v body %s", got, body)
	}
	status, err = s.Send(context.Background(), srv.URL+"/moved", nil, []byte(`{}`))
	if err != nil || status != http.StatusTemporaryRedirect {
		t.Fatalf("a redirect: %d %v", status, err)
	}
	if _, err := s.Send(context.Background(), "ftp://example.com/x", nil, nil); err == nil {
		t.Fatal("an ftp endpoint was sent to")
	}
}
