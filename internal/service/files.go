// Example file: delete the whole file after forking

package service

import (
	"net/http"
	"strings"

	"github.com/go-keel/keel/server/api"
)

// demoFileContent is embedded demo content; in real use replace it with a ReadSeeker over object storage or a local file.
const demoFileContent = "keel layout demo file\n"

// DemoDownloadHandler shows the raw download path (2c): it holds the ResponseWriter directly;
// api.ServeContent supports Range, resumable downloads and conditional GET; the file name is escaped per RFC 6266.
// To require login, the mounting code registers it with sc.RequireLogin() instead (see internal/server/http.go).
func DemoDownloadHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Raw routes skip codegen; without a method check, POST/PUT could fetch the file too.
		// The download allows GET and HEAD: ServeContent supports conditional GET and Range,
		// and HEAD is the usual probe for size before a resumable download.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		api.ServeContent(w, r, strings.NewReader(demoFileContent), api.DownloadOptions{
			Filename:    "demo.txt",
			ContentType: "text/plain; charset=utf-8",
		})
	}
}
