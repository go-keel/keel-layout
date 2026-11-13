package i18n

import (
	"embed"
	"errors"
	"io/fs"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"

	"github.com/go-keel/keel/middleware/locale"
)

//go:embed *.toml
var localeFS embed.FS

func NewBundle() *locale.Bundle {
	b := i18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	loadMessageFiles(b, localeFS)
	return locale.NewBundle(b)
}

// loadMessageFiles loads every *.toml in the directory: a derived repo drops in a new language file and it takes
// effect without changing this code (the file-name prefix is not limited to active; sys.*.toml / app.*.toml work
// too). It skips the translation drafts translate.*.toml exported by make i18n-merge: they sort after active.* and
// would load later, overwriting the real translations with the same IDs. Language files are static assets embedded
// in the binary, so a load failure can only mean the file itself is broken; swallowing the error would silently
// turn all text into message IDs, with no way to tell "one entry missing" from "the whole file not loaded". So it
// panics and fails at startup.
func loadMessageFiles(b *i18n.Bundle, fsys fs.FS) []*i18n.MessageFile {
	names, err := fs.Glob(fsys, "*.toml")
	if err != nil {
		panic("i18n: glob *.toml: " + err.Error())
	}
	var files []*i18n.MessageFile
	for _, name := range names {
		if strings.HasPrefix(name, "translate.") {
			continue
		}
		f, err := b.LoadMessageFileFS(fsys, name)
		if err != nil {
			panic("i18n: load " + name + ": " + err.Error())
		}
		files = append(files, f)
	}
	return files
}

// MustLocalizeMessage is like (*i18n.Localizer).MustLocalizeMessage, but does not treat "the current language lacks
// this entry" as a failure: on a missing entry go-i18n already falls back to Message.Other (the English source) and
// returns *i18n.MessageNotFoundErr. That is the designed fallback, not a load problem; only other errors (template
// parse failure, ID mismatch and so on) panic.
func MustLocalizeMessage(localizer *i18n.Localizer, msg *i18n.Message) string {
	localized, err := localizer.LocalizeMessage(msg)
	if err != nil {
		var notFound *i18n.MessageNotFoundErr
		if !errors.As(err, &notFound) {
			panic(err)
		}
	}
	return localized
}
