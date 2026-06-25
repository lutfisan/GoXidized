package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCSettings struct {
	IssuerURL       string
	ClientID        string
	ClientSecret    string
	RedirectURL     string
	Scopes          []string
	RequireVerified bool
}

type OIDCIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
}

type OIDCAuthenticator interface {
	Issuer() string
	AuthCodeURL(state, nonce string) string
	Exchange(ctx context.Context, code, nonce string) (OIDCIdentity, error)
}

type goOIDCAuthenticator struct {
	issuer       string
	oauth2Config *oauth2.Config
	verifier     *oidc.IDTokenVerifier
}

func NewOIDCAuthenticator(ctx context.Context, settings OIDCSettings) (OIDCAuthenticator, error) {
	if settings.IssuerURL == "" {
		return nil, errors.New("oidc issuer URL is required")
	}
	if settings.ClientID == "" {
		return nil, errors.New("oidc client ID is required")
	}
	if settings.ClientSecret == "" {
		return nil, errors.New("oidc client secret is required")
	}
	if len(settings.Scopes) == 0 {
		settings.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	provider, err := oidc.NewProvider(ctx, settings.IssuerURL)
	if err != nil {
		return nil, err
	}
	return &goOIDCAuthenticator{
		issuer: settings.IssuerURL,
		oauth2Config: &oauth2.Config{
			ClientID:     settings.ClientID,
			ClientSecret: settings.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  settings.RedirectURL,
			Scopes:       settings.Scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: settings.ClientID}),
	}, nil
}

func (a *goOIDCAuthenticator) Issuer() string {
	return a.issuer
}

func (a *goOIDCAuthenticator) AuthCodeURL(state, nonce string) string {
	return a.oauth2Config.AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", nonce))
}

func (a *goOIDCAuthenticator) Exchange(ctx context.Context, code, nonce string) (OIDCIdentity, error) {
	token, err := a.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return OIDCIdentity{}, err
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return OIDCIdentity{}, errors.New("oidc provider did not return an id_token")
	}
	idToken, err := a.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return OIDCIdentity{}, err
	}
	var claims struct {
		Subject           string `json:"sub"`
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Nonce             string `json:"nonce"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return OIDCIdentity{}, err
	}
	if claims.Subject == "" {
		claims.Subject = idToken.Subject
	}
	if claims.Subject == "" {
		return OIDCIdentity{}, errors.New("oidc id_token subject is empty")
	}
	if nonce != "" && claims.Nonce != nonce {
		return OIDCIdentity{}, fmt.Errorf("oidc nonce mismatch")
	}
	displayName := claims.Name
	if displayName == "" {
		displayName = claims.PreferredUsername
	}
	if displayName == "" {
		displayName = claims.Email
	}
	return OIDCIdentity{
		Subject:       claims.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		DisplayName:   displayName,
	}, nil
}
