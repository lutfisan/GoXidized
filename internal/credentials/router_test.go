package credentials

import (
	"context"
	"strings"
	"testing"

	"goxidized/pkg/goxidized"
)

type staticProvider struct{}

func (staticProvider) Resolve(context.Context, string) (goxidized.Credentials, error) {
	return goxidized.Credentials{Username: "u", Password: goxidized.NewSecretString("p"), Source: "static"}, nil
}

func TestRouterRejectsUnknownScheme(t *testing.T) {
	r := Router{Default: staticProvider{}, Providers: map[string]goxidized.CredentialProvider{"static": staticProvider{}}}
	_, err := r.Resolve(context.Background(), "vault://not-configured")
	if err == nil || !strings.Contains(err.Error(), "unknown credential provider") {
		t.Fatalf("err=%v, want unknown provider", err)
	}
}

func TestRouterUsesDefaultForBareRef(t *testing.T) {
	r := Router{Default: staticProvider{}, Providers: map[string]goxidized.CredentialProvider{"static": staticProvider{}}}
	creds, err := r.Resolve(context.Background(), "R1")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Source != "static" {
		t.Fatalf("source=%s, want static", creds.Source)
	}
}
