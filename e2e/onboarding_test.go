//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

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

	newPass := `(//input[@type="password"])`
	err := chromedp.Do(ctx,
		chromedp.Navigate(fb.URL+"/control"),
		chromedp.WaitVisible(`//h1[text()="Welcome to Fresh Breath"]`),
		chromedp.SendKeys(chromedp.CSS(`input[name="name"]`), "Ada Lovelace"),
		chromedp.SendKeys(chromedp.CSS(`input[name="email"]`), "ada@example.com"),
		chromedp.SendKeys(newPass+`[1]`, "analytical engine"),
		chromedp.SendKeys(newPass+`[2]`, "analytical engine"),
		chromedp.Click(chromedp.CSS(`form button[type="submit"]`)),
		chromedp.WaitVisible(`//h1[text()="You're all set"]`),
		// The panel is gated now, so the reload sends the whole tab to the
		// SSH sign-in page and back.
		chromedp.Click(`//button[contains(., "Continue to sign in")]`),
		chromedp.WaitVisible(chromedp.ID("e")),
		chromedp.SendKeys(chromedp.ID("e"), "ada@example.com"),
		chromedp.SendKeys(chromedp.ID("p"), "analytical engine"),
		chromedp.Click(chromedp.ID("btn")),
	)
	if err != nil {
		t.Fatalf("onboarding: %v", err)
	}

	type meResult struct {
		User struct{ Email, Role string }
	}
	var me meResult
	err = chromedp.Do(ctx, chromedp.WaitVisible(chromedp.CSS(`.app-shell`)))
	if err == nil {
		me, err = chromedp.Run(ctx, chromedp.Evaluate[meResult](
			`FrBr.api(FrBr.currentSession(), "/api/me").then(r => r.json())`, chromedp.EvalAwaitPromise))
	}
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

	var continueLink chromedp.AttributeResult
	var caPEM string
	err := chromedp.Do(ctx,
		chromedp.Navigate(fb.URL+"/control"),
		chromedp.WaitVisible(`//h1[text()="Secure the connection"]`),
		chromedp.Click(`//button[contains(., "Generate a certificate")]`),
		chromedp.WaitVisible(`//h1[text()="Certificate ready"]`),
	)
	if err == nil {
		continueLink, err = chromedp.Run(ctx, chromedp.AttributeValue(`//a[contains(., "Continue at")]`, "href"))
	}
	if err == nil {
		caPEM, err = chromedp.Run(ctx, chromedp.Evaluate[string](
			`fetch("/api/tls/ca.pem").then(r => r.text())`, chromedp.EvalAwaitPromise))
	}
	if err != nil {
		t.Fatalf("certificate step: %v", err)
	}
	if want := strings.Replace(fb.URL, "http://", "https://", 1) + "/control"; continueLink.Value != want {
		t.Errorf("continue link = %q, want %q", continueLink.Value, want)
	}
	if !strings.HasPrefix(caPEM, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("CA download = %.40q", caPEM)
	}
	env, _ := os.ReadFile(fb.EnvFile)
	if !strings.Contains(string(env), "FRBR_TLS_CERT=") || !strings.Contains(string(env), "FRBR_TLS_KEY=") {
		t.Errorf("env file doesn't point at the cert:\n%s", env)
	}

	// Skipping instead goes straight on to the account.
	err = chromedp.Do(ctx,
		chromedp.Navigate(fb.URL+"/control"),
		chromedp.Click(`//button[contains(., "Skip — stay on HTTP")]`),
		chromedp.WaitVisible(`//h1[text()="Welcome to Fresh Breath"]`),
	)
	if err != nil {
		t.Fatalf("skipping the certificate: %v", err)
	}
}
