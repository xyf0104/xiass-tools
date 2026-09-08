package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"antigravity-wf-assistant/internal/oauthflow"
)

const oauthRefreshLeadTime = 5 * time.Minute

var oauthRefreshRun = struct {
	sync.Mutex
	inFlight map[string]*oauthRefreshCall
}{inFlight: make(map[string]*oauthRefreshCall)}

type oauthRefreshCall struct {
	done chan struct{}
	err  error
}

// EnsureAccountAccessToken refreshes a near-expiry OAuth access token once
// per account. It is intentionally a storage-level operation so proxy account
// scheduling can use it without reaching into the Wails application layer.
func EnsureAccountAccessToken(ctx context.Context, accountID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	account, err := GetUpstreamAccount(accountID)
	now := time.Now()
	if err != nil || !needsOAuthRefresh(account, now) || oauthRefreshCoolingDown(account, now) {
		return err
	}

	oauthRefreshRun.Lock()
	if current := oauthRefreshRun.inFlight[accountID]; current != nil {
		oauthRefreshRun.Unlock()
		select {
		case <-current.done:
			return current.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := &oauthRefreshCall{done: make(chan struct{})}
	oauthRefreshRun.inFlight[accountID] = call
	oauthRefreshRun.Unlock()

	call.err = refreshStoredOAuthAccount(ctx, accountID)
	close(call.done)
	oauthRefreshRun.Lock()
	delete(oauthRefreshRun.inFlight, accountID)
	oauthRefreshRun.Unlock()
	return call.err
}

func needsOAuthRefresh(account UpstreamAccount, now time.Time) bool {
	if normalizeAccountType(account.Type) != "oauth" {
		return false
	}
	expiresAt, ok := parseAccountTime(account.AuthExpiresAt)
	return ok && !expiresAt.After(now.Add(oauthRefreshLeadTime))
}

// oauthRefreshCoolingDown avoids repeatedly calling a failed token endpoint
// while the account scheduler has deliberately taken that account out of its
// pool. A subsequent lease selection will still skip the account via
// accountUsable until the cooldown has passed.
func oauthRefreshCoolingDown(account UpstreamAccount, now time.Time) bool {
	until, ok := parseAccountTime(account.CooldownUntil)
	return ok && until.After(now)
}

func refreshStoredOAuthAccount(ctx context.Context, accountID string) error {
	account, err := GetUpstreamAccount(accountID)
	if err != nil {
		return err
	}
	if !needsOAuthRefresh(account, time.Now()) {
		return nil
	}
	refreshToken := accountCredential(account.Credentials, "refresh_token", "refreshToken", "mobile_rt", "mobileRT")
	if refreshToken == "" {
		err := fmt.Errorf("OAuth 访问令牌已到期且没有刷新令牌")
		markOAuthRefreshFailure(accountID, err)
		return err
	}
	flow, err := oauthflow.New(oauthflow.Config{
		AuthorizationURL: account.OAuth.AuthorizationURL,
		TokenURL:         account.OAuth.TokenURL,
		PublicClientID:   account.OAuth.ClientID,
		RedirectURI:      account.OAuth.RedirectURI,
		Scopes:           strings.Fields(account.OAuth.Scopes),
		RefreshScopes:    strings.Fields(account.OAuth.RefreshScopes),
	})
	if err != nil {
		markOAuthRefreshFailure(accountID, err)
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	token, err := flow.Refresh(refreshCtx, refreshToken)
	if err != nil {
		markOAuthRefreshFailure(accountID, err)
		return err
	}

	credentials := cloneAnyMap(account.Credentials)
	if credentials == nil {
		credentials = make(map[string]any)
	}
	credentials["access_token"] = token.AccessToken
	if token.RefreshToken != "" {
		credentials["refresh_token"] = token.RefreshToken
	}
	if token.IDToken != "" {
		credentials["id_token"] = token.IDToken
	}
	if token.TokenType != "" {
		credentials["token_type"] = token.TokenType
	}
	if token.Scope != "" {
		credentials["scope"] = token.Scope
	}
	account.APIKey = token.AccessToken
	account.Credentials = credentials
	if token.ExpiresAt.IsZero() {
		account.AuthExpiresAt = ""
	} else {
		account.AuthExpiresAt = token.ExpiresAt.UTC().Format(time.RFC3339)
	}
	account.Identity.Source = "OAuth 刷新响应"
	account.Identity.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := SaveUpstreamAccount(account); err != nil {
		return err
	}
	return nil
}

func markOAuthRefreshFailure(accountID string, refreshErr error) {
	_ = updateUpstreamAccount(accountID, func(account *UpstreamAccount) {
		account.LastError = "OAuth 刷新失败：" + shortAccountError(refreshErr.Error(), 0)
		account.FailureCount++
		account.CooldownUntil = time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	})
}

func accountCredential(credentials map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := credentials[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
