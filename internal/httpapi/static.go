package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"feedrank/api"
	"feedrank/web"
)

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(api.OpenAPI)
}

// docsPage renders the embedded OpenAPI document with Swagger UI, which also
// lets a reviewer try the endpoints from the browser.
const docsPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Feedrank API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
  <script>SwaggerUIBundle({ url: "/openapi.yaml", dom_id: "#swagger-ui" });</script>
</body>
</html>`

func serveDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsPage))
}

// dashboard serves the embedded React build. Vite fingerprints the files in
// assets/, so those can be cached for good; index.html is always revalidated
// so a new deploy is picked up.
func dashboard(log *slog.Logger) http.Handler {
	assets, ok := web.Dashboard()
	if !ok {
		log.Warn("dashboard is not built; run `make dashboard` (Docker builds it automatically)")
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "The dashboard has not been built. Run `make dashboard`, or use `make up`.", http.StatusNotFound)
		})
	}

	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
