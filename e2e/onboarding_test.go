//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// A fresh install opens on onboarding. Filling it in makes the first
// Superuser and locks the panel; signing back in with the new passphrase,
// through the real SSH sign-in page, lands in the panel as that user.
func TestOnboardingThenSignIn(t *testing.T) {
	fb := startFreshbreath(t)
	browser := newBrowser(t)
	ctx, cancel := context.WithTimeout(browser, 45*time.Second)
	defer cancel()

	newPass := `(//input[@autocomplete="new-password"])`
	err := chromedp.Run(ctx,
		chromedp.Navigate(fb.URL+"/control"),
		chromedp.WaitVisible(`//h1[text()="Welcome to Fresh Breath"]`, chromedp.BySearch),
		chromedp.SendKeys(`input[autocomplete="name"]`, "Ada Lovelace", chromedp.ByQuery),
		chromedp.SendKeys(`input[autocomplete="email"]`, "ada@example.com", chromedp.ByQuery),
		chromedp.SendKeys(newPass+`[1]`, "analytical engine", chromedp.BySearch),
		chromedp.SendKeys(newPass+`[2]`, "analytical engine", chromedp.BySearch),
		chromedp.Click(`form button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`//h1[text()="You're all set"]`, chromedp.BySearch),
		// The panel is gated now, so the reload sends the whole tab to the
		// SSH sign-in page and back.
		chromedp.Click(`//button[contains(., "Continue to sign in")]`, chromedp.BySearch),
		chromedp.WaitVisible(`#e`, chromedp.ByID),
		chromedp.SendKeys(`#e`, "ada@example.com", chromedp.ByID),
		chromedp.SendKeys(`#p`, "analytical engine", chromedp.ByID),
		chromedp.Click(`#btn`, chromedp.ByID),
	)
	if err != nil {
		t.Fatalf("onboarding: %v", err)
	}

	var me struct {
		User struct{ Email, Role string }
	}
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`.app-shell`, chromedp.ByQuery),
		chromedp.Evaluate(`FrBr.api(FrBr.currentSession(), "/api/me").then(r => r.json())`, &me,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
	)
	if err != nil {
		t.Fatalf("panel after sign-in: %v", err)
	}
	if me.User.Email != "ada@example.com" || me.User.Role != "Superuser" {
		t.Errorf("signed in as %+v, want ada@example.com as Superuser", me.User)
	}
}

// On plain HTTP, onboarding offers HTTPS first: one click issues a cert
// from the install's own CA, records it in the env file, and offers the
// CA for download along with the https address to continue at.
func TestOnboardingIssuesLocalCertificate(t *testing.T) {
	fb := startPlainFreshbreath(t)
	browser := newBrowser(t)
	ctx, cancel := context.WithTimeout(browser, 45*time.Second)
	defer cancel()

	var continueURL string
	var caPEM string
	err := chromedp.Run(ctx,
		chromedp.Navigate(fb.URL+"/control"),
		chromedp.WaitVisible(`//h1[text()="Secure the connection"]`, chromedp.BySearch),
		chromedp.Click(`//button[contains(., "Generate a certificate")]`, chromedp.BySearch),
		chromedp.WaitVisible(`//h1[text()="Certificate ready"]`, chromedp.BySearch),
		chromedp.AttributeValue(`//a[contains(., "Continue at")]`, "href", &continueURL, nil, chromedp.BySearch),
		chromedp.Evaluate(`fetch("/api/tls/ca.pem").then(r => r.text())`, &caPEM,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
	)
	if err != nil {
		t.Fatalf("certificate step: %v", err)
	}
	if want := strings.Replace(fb.URL, "http://", "https://", 1) + "/control"; continueURL != want {
		t.Errorf("continue link = %q, want %q", continueURL, want)
	}
	if !strings.HasPrefix(caPEM, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("CA download = %.40q", caPEM)
	}
	env, _ := os.ReadFile(fb.EnvFile)
	if !strings.Contains(string(env), "FRBR_TLS_CERT=") || !strings.Contains(string(env), "FRBR_TLS_KEY=") {
		t.Errorf("env file doesn't point at the cert:\n%s", env)
	}

	// Skipping instead goes straight on to the account.
	err = chromedp.Run(ctx,
		chromedp.Navigate(fb.URL+"/control"),
		chromedp.Click(`//button[contains(., "Skip — stay on HTTP")]`, chromedp.BySearch),
		chromedp.WaitVisible(`//h1[text()="Welcome to Fresh Breath"]`, chromedp.BySearch),
	)
	if err != nil {
		t.Fatalf("skipping the certificate: %v", err)
	}
}
