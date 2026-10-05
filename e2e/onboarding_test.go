//go:build e2e

package e2e

import (
	"context"
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
