// Shared file: carried verbatim; never localized outside its source repository.
// Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md.

package i18n

import (
	"embed"
	"io/fs"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed *.toml
var localeFS embed.FS

// NewBundle returns the go-i18n bundle with this repository's message files loaded; English is the default language,
// the one a request without a supported language gets.
func NewBundle() *i18n.Bundle {
	b := i18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	loadMessageFiles(b, localeFS)
	return b
}

// loadMessageFiles loads every *.toml in the directory, whatever the file-name prefix (active.*, sys.*, app.*): a
// derived repo drops in a new language file and it takes effect without changing this code. It skips the translation
// drafts translate.*.toml exported by make i18n-merge: they sort later and would load later, overwriting the real
// translations with the same IDs. Language files are static assets embedded in the binary, so a load failure can only
// mean the file itself is broken; swallowing the error would silently turn all text into English, with no way to tell
// "one entry missing" from "the whole file not loaded". So it panics and fails at startup.
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
