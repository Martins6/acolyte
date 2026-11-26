package routine

import (
	"fmt"
	"net/http"
	"strings"
)

// routineSettlementStub answers the headless settlement list routes with empty
// results so routine tests exercise their own turn scripts, not the settler.
func routineSettlementStub(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/form"):
			fmt.Fprint(w, `{"data":[]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/permission"):
			fmt.Fprint(w, `{"data":[]}`)
		case r.Method == http.MethodGet && path == "/api/session" && r.URL.Query().Get("parentID") != "":
			fmt.Fprint(w, `{"data":[],"cursor":{"previous":null,"next":null}}`)
		default:
			handler(w, r)
		}
	}
}
