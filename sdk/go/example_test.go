package zoikotax_test

import (
	"context"
	"errors"
	"log/slog"

	zoikotax "github.com/zoikogroup/zoikotax/sdk/go"
)

func Example() {
	client, err := zoikotax.NewClient("https://eu-west-1.zoikotax.com")
	if err != nil {
		slog.Error("configuring the client", "err", err)
		return
	}

	caps, err := client.GetCapabilities(context.Background())
	var zerr *zoikotax.ZoikoTaxError
	switch {
	case errors.As(err, &zerr):
		slog.Error("the cell refused", "reason", zerr.ReasonCode, "request", zerr.RequestID)
	case err != nil:
		slog.Error("the cell was not reached", "err", err)
	case !caps.Authoritative:
		// Before A4 no deployment may produce authoritative fiscal output.
		// Figures are advisory and must not be filed.
	}
}

func ExampleZoikoTaxError() {
	client, _ := zoikotax.NewClient("https://eu-west-1.zoikotax.com")

	_, err := client.CreateUser(context.Background(), zoikotax.CreateUserRequest{
		Email:       "auditor@acme.example",
		DisplayName: "Katherine Johnson",
		Roles:       []zoikotax.Role{zoikotax.RoleAUDITOR},
	})

	// Branch on the reason code, never on the title or the detail.
	var zerr *zoikotax.ZoikoTaxError
	if errors.As(err, &zerr) && zerr.ReasonCode == "ALREADY_EXISTS" {
		return // already invited
	}
}
