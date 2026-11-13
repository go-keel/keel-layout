package i18n

import (
	"testing"
	"testing/fstest"

	"github.com/BurntSushi/toml"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// TestBundleLoadsAllLocaleFiles runs the language-file loading that startup does: a broken file panics and fails
// here, not after deployment. It hard-codes no language or entry: for each file it takes the file's own first entry
// and asserts it resolves in that file's language, so a derived repo's partially translated file passes too.
func TestBundleLoadsAllLocaleFiles(t *testing.T) {
	b := goi18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	files := loadMessageFiles(b, localeFS)
	if len(files) == 0 {
		t.Fatal("no embedded language files; the test asserts nothing")
	}

	for _, f := range files {
		if len(f.Messages) == 0 {
			continue
		}
		loc := goi18n.NewLocalizer(b, f.Tag.String())
		if _, err := loc.Localize(&goi18n.LocalizeConfig{MessageID: f.Messages[0].ID}); err != nil {
			t.Errorf("%s: language file not loaded into the bundle: %v", f.Path, err)
		}
	}
}

// TestLoadMessageFiles_SkipsTranslateDrafts: when translation drafts translate.*.toml sit next to the real files,
// the runtime gets the real translation. Without the skip, the draft loads later and overwrites it, and this fails.
func TestLoadMessageFiles_SkipsTranslateDrafts(t *testing.T) {
	fsys := fstest.MapFS{
		"active.zh-CN.toml":    {Data: []byte(`Greeting = "你好"`)},
		"translate.zh-CN.toml": {Data: []byte("[Greeting]\nhash = \"sha1-x\"\nother = \"hello\"\n")},
	}
	b := goi18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	loadMessageFiles(b, fsys)

	got, err := goi18n.NewLocalizer(b, "zh-CN").Localize(&goi18n.LocalizeConfig{MessageID: "Greeting"})
	if err != nil || got != "你好" {
		t.Fatalf("expected 你好, got %q (err %v)", got, err)
	}
}

// TestMustLocalizeMessage_FallsBackToEnglish builds a language missing an entry: the Localizer picks French, but the
// bundle has the message only in English. It asserts the English source comes back with no error and no panic;
// replacing MustLocalizeMessage with localizer.MustLocalizeMessage should make this test fail with a panic.
func TestMustLocalizeMessage_FallsBackToEnglish(t *testing.T) {
	b := goi18n.NewBundle(language.English)
	if err := b.AddMessages(language.English, &goi18n.Message{ID: "Greeting", Other: "hello"}); err != nil {
		t.Fatalf("add english message: %v", err)
	}
	// French is registered in the bundle (otherwise language matching falls straight back to the default language and
	// the "language present, entry missing" path is not tested), but it has no Greeting entry.
	if err := b.AddMessages(language.French, &goi18n.Message{ID: "Unrelated", Other: "bonjour"}); err != nil {
		t.Fatalf("add french message: %v", err)
	}

	loc := goi18n.NewLocalizer(b, "fr")
	got := MustLocalizeMessage(loc, &goi18n.Message{ID: "Greeting", Other: "hello"})
	if got != "hello" {
		t.Fatalf("expected english fallback %q, got %q", "hello", got)
	}
}
