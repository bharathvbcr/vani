package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/vault"
)

// The variables an Adzuna board's credentials come from (docs/SETUP.md). The
// Mac app reads the Keychain item and passes both to a run it starts.
const (
	adzunaAppIDEnv  = "VANI_ADZUNA_APP_ID"
	adzunaAppKeyEnv = "VANI_ADZUNA_APP_KEY"
)

// adzunaCredentials is the owner's Adzuna app id and key for an Adzuna board
// (adzuna.Options.Credentials): both variables when both are set, else, unless
// VANI_NO_KEYCHAIN is set, the login Keychain item vault.AdzunaService holding
// "APP_ID:APP_KEY", read and cached as modelKey reads a model key. Only the
// adzuna adapter receives them, and it sends them only to api.adzuna.com. No
// error carries a value.
func (w *wired) adzunaCredentials(_ context.Context) (appID, appKey string, err error) {
	appID = strings.TrimSpace(os.Getenv(adzunaAppIDEnv))
	appKey = strings.TrimSpace(os.Getenv(adzunaAppKeyEnv))
	switch {
	case appID != "" && appKey != "":
		return appID, appKey, nil
	case appID != "" || appKey != "":
		return "", "", fmt.Errorf("adzuna: only one of %s and %s is set; set both", adzunaAppIDEnv, adzunaAppKeyEnv)
	}
	v, _, why := w.modelKey("adzuna", []string{adzunaAppIDEnv, adzunaAppKeyEnv}, vault.AdzunaService)
	if v == "" {
		return "", "", fmt.Errorf("adzuna: no app id and key: set %s and %s, or keep \"APP_ID:APP_KEY\" in the login Keychain item %q (docs/SETUP.md); %s",
			adzunaAppIDEnv, adzunaAppKeyEnv, vault.AdzunaService, why)
	}
	id, key, ok := strings.Cut(v, ":")
	id, key = strings.TrimSpace(id), strings.TrimSpace(key)
	if !ok || id == "" || key == "" {
		return "", "", fmt.Errorf("adzuna: the login Keychain item %q is not \"APP_ID:APP_KEY\"", vault.AdzunaService)
	}
	return id, key, nil
}

// adzunaKeyNote is `config check`'s reminder when the watchlist has an Adzuna
// board and neither credential variable is set: the run will read the login
// Keychain item, which config check does not (it never prompts), and the
// board fails each run without one. "" when there is nothing to say.
func adzunaKeyNote(wl []sources.Board, getenv func(string) string) string {
	has := false
	for _, b := range wl {
		if b.ATS == posting.Adzuna {
			has = true
			break
		}
	}
	if !has || (strings.TrimSpace(getenv(adzunaAppIDEnv)) != "" && strings.TrimSpace(getenv(adzunaAppKeyEnv)) != "") {
		return ""
	}
	return fmt.Sprintf("the watchlist has an Adzuna board; a run needs %s and %s, or \"APP_ID:APP_KEY\" in the login Keychain item %q (not read here); without them that board fails each run (docs/SETUP.md)",
		adzunaAppIDEnv, adzunaAppKeyEnv, vault.AdzunaService)
}
