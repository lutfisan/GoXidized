package credentials

import (
	"context"
	"fmt"
	"strings"

	"goxidized/pkg/goxidized"
)

type Router struct {
	Default   goxidized.CredentialProvider
	Providers map[string]goxidized.CredentialProvider
}

func (r Router) Resolve(ctx context.Context, ref string) (goxidized.Credentials, error) {
	providerName := ""
	if i := strings.Index(ref, "://"); i > 0 {
		providerName = ref[:i]
	}
	if providerName != "" {
		if p, ok := r.Providers[providerName]; ok {
			return p.Resolve(ctx, ref)
		}
		return goxidized.Credentials{}, fmt.Errorf("unknown credential provider scheme %q", providerName)
	}
	if r.Default == nil {
		return goxidized.Credentials{}, fmt.Errorf("no credential provider configured for %q", ref)
	}
	return r.Default.Resolve(ctx, ref)
}
