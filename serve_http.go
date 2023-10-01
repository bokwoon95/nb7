package nb7

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/bokwoon95/sq"
)

var defaultLogger = slog.New(NewJSONHandler(os.Stdout, os.Stderr, &slog.HandlerOptions{
	AddSource: true,
}))

func (nbrew *Notebrew) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Clean the path and redirect if necessary.
	if r.Method == "GET" {
		cleanedPath := path.Clean(r.URL.Path)
		if cleanedPath != "/" && path.Ext(cleanedPath) == "" {
			cleanedPath += "/"
		}
		if cleanedPath != r.URL.Path {
			cleanedURL := *r.URL
			cleanedURL.Path = cleanedPath
			http.Redirect(w, r, cleanedURL.String(), http.StatusMovedPermanently)
			return
		}
	}

	// Inject the request method and url into the logger.
	logger := nbrew.Logger
	if logger == nil {
		logger = defaultLogger
	}
	scheme := "https://"
	if r.TLS == nil {
		scheme = "http://"
	}
	ip := getIP(r)
	logger = logger.With(
		slog.String("method", r.Method),
		slog.String("url", scheme+r.Host+r.URL.RequestURI()),
		slog.String("ip", ip),
	)
	r = r.WithContext(context.WithValue(r.Context(), loggerKey, logger))

	// https://cheatsheetseries.owasp.org/cheatsheets/HTTP_Headers_Cheat_Sheet.html
	w.Header().Add("X-Frame-Options", "DENY")
	w.Header().Add("X-Content-Type-Options", "nosniff")
	w.Header().Add("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Add("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
	w.Header().Add("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Add("Cross-Origin-Embedder-Policy", "require-corp")
	w.Header().Add("Cross-Origin-Resource-Policy", "same-site")
	if nbrew.Scheme == "https://" {
		w.Header().Add("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
	}

	host := getHost(r)
	head, tail, _ := strings.Cut(strings.Trim(r.URL.Path, "/"), "/")
	if host == nbrew.AdminDomain && head == "admin" {
		nbrew.admin(w, r, ip)
		return
	}

	var subdomainPrefix string
	var sitePrefix string
	var customDomain string
	if strings.HasSuffix(host, "."+nbrew.ContentDomain) {
		subdomainPrefix = strings.TrimSuffix(host, "."+nbrew.ContentDomain)
	} else if host != nbrew.ContentDomain {
		customDomain = host
	}
	if strings.HasPrefix(head, "@") {
		sitePrefix = head
	}
	if sitePrefix != "" && (subdomainPrefix != "" || customDomain != "") {
		notFound(w, r)
		return
	}
	resourcePath := strings.Trim(r.URL.Path, "/")
	if sitePrefix != "" {
		resourcePath = tail
		siteName := strings.TrimPrefix(sitePrefix, "@")
		for _, char := range siteName {
			if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '-' {
				continue
			}
			notFound(w, r)
			return
		}
		if nbrew.MultisiteMode == "subdomain" {
			http.Redirect(w, r, nbrew.Scheme+siteName+"."+nbrew.ContentDomain+"/"+resourcePath, http.StatusFound)
			return
		}
	} else if subdomainPrefix != "" {
		sitePrefix = "@" + subdomainPrefix
		for _, char := range subdomainPrefix {
			if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '-' {
				continue
			}
			notFound(w, r)
			return
		}
		if nbrew.MultisiteMode == "subdirectory" {
			http.Redirect(w, r, nbrew.Scheme+nbrew.ContentDomain+"/"+path.Join(sitePrefix, resourcePath), http.StatusFound)
			return
		}
	} else if customDomain != "" {
		sitePrefix = customDomain
		fileInfo, err := fs.Stat(nbrew.FS, customDomain)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				notFound(w, r)
				return
			}
			logger.Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if !fileInfo.IsDir() {
			notFound(w, r)
			return
		}
	}
	if nbrew.MultisiteMode == "" && sitePrefix != "" {
		notFound(w, r)
		return
	}
	w.Write([]byte("<!DOCTYPE html><title>Hello world!</title>Hello world!"))
	// nbrew.content(w, r, sitePrefix, resourcePath)
}

type JSONHandler struct {
	stdoutHandler slog.Handler
	stderrHandler slog.Handler
}

func NewJSONHandler(stdout io.Writer, stderr io.Writer, opts *slog.HandlerOptions) *JSONHandler {
	return &JSONHandler{
		stdoutHandler: slog.NewJSONHandler(stdout, opts),
		stderrHandler: slog.NewJSONHandler(stderr, opts),
	}
}

func (h *JSONHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.stderrHandler.Enabled(ctx, level)
}

func (h *JSONHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level == slog.LevelError {
		return h.stderrHandler.Handle(ctx, record)
	}
	return h.stdoutHandler.Handle(ctx, record)
}

func (h *JSONHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &JSONHandler{
		stdoutHandler: h.stdoutHandler.WithAttrs(attrs),
		stderrHandler: h.stderrHandler.WithAttrs(attrs),
	}
}

func (h *JSONHandler) WithGroup(name string) slog.Handler {
	return &JSONHandler{
		stdoutHandler: h.stdoutHandler.WithGroup(name),
		stderrHandler: h.stderrHandler.WithGroup(name),
	}
}

func (nbrew *Notebrew) admin(w http.ResponseWriter, r *http.Request, ip string) {
	urlPath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin"), "/")
	head, tail, _ := strings.Cut(urlPath, "/")
	if head == "static" {
		serveFile(w, r, rootFS, urlPath, true)
		return
	}
	if head == "signup" || head == "login" || head == "logout" || head == "resetpassword" {
		if tail != "" {
			notFound(w, r)
			return
		}
		switch head {
		case "signup":
			nbrew.signup(w, r, ip)
		case "login":
			nbrew.login(w, r, ip)
		case "logout":
			nbrew.logout(w, r, ip)
		case "resetpassword":
			nbrew.resetpassword(w, r, ip)
		}
		return
	}

	var sitePrefix string
	if strings.HasPrefix(head, "@") || strings.Contains(head, ".") {
		sitePrefix, urlPath = head, tail
		head, tail, _ = strings.Cut(urlPath, "/")
	}

	if head == "themes" || head == "images" {
		serveFile(w, r, nbrew.FS, path.Join(sitePrefix, "site", urlPath), true)
		return
	}

	var username string
	if nbrew.DB != nil {
		authenticationTokenHash := getAuthenticationTokenHash(r)
		if authenticationTokenHash == nil {
			if head == "" {
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/login/?401", http.StatusFound)
				return
			}
			notAuthenticated(w, r)
			return
		}
		result, err := sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "SELECT {*}" +
				" FROM authentication" +
				" JOIN users ON users.user_id = authentication.user_id" +
				" LEFT JOIN (" +
				"SELECT site_user.user_id" +
				" FROM site_user" +
				" JOIN site ON site.site_id = site_user.site_id" +
				" WHERE site.site_name = {siteName}" +
				") AS authorized_users ON authorized_users.user_id = users.user_id" +
				" WHERE authentication.authentication_token_hash = {authenticationTokenHash}" +
				" LIMIT 1",
			Values: []any{
				sq.StringParam("siteName", strings.TrimPrefix(sitePrefix, "@")),
				sq.BytesParam("authenticationTokenHash", authenticationTokenHash),
			},
		}, func(row *sq.Row) (result struct {
			Username     string
			IsAuthorized bool
		}) {
			result.Username = row.String("users.username")
			result.IsAuthorized = row.Bool("authorized_users.user_id IS NOT NULL")
			return result
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.SetCookie(w, &http.Cookie{
					Path:   "/",
					Name:   "authentication",
					Value:  "0",
					MaxAge: -1,
				})
				if head == "" {
					http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/login/?401", http.StatusFound)
					return
				}
				notAuthenticated(w, r)
				return
			}
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		username = result.Username
		logger := getLogger(r.Context()).With(slog.String("username", username))
		r = r.WithContext(context.WithValue(r.Context(), loggerKey, logger))
		isRootPage := sitePrefix == "" && head == "" // everyone is allowed to access the root page
		if !result.IsAuthorized && !isRootPage {
			notAuthorized(w, r)
			return
		}
	}

	if head == "" || head == "notes" || head == "pages" || head == "posts" || head == "public" {
		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, urlPath))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				notFound(w, r)
				return
			}
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if fileInfo.IsDir() {
			nbrew.folder(w, r, username, sitePrefix, urlPath, fileInfo)
			return
		}
		nbrew.file(w, r, username, sitePrefix, urlPath, fileInfo)
		return
	}

	if tail != "" {
		notFound(w, r)
		return
	}
	switch head {
	case "createsite":
		nbrew.createsite(w, r, username)
	case "deletesite":
		nbrew.deletesite(w, r, username)
	case "delete":
		nbrew.delet(w, r, username, sitePrefix)
	case "createnote":
		nbrew.createnote(w, r, username, sitePrefix)
	case "createpost":
		nbrew.createpost(w, r, username, sitePrefix)
	case "createcategory":
		nbrew.createcategory(w, r, username, sitePrefix)
	case "createfolder":
		nbrew.createfolder(w, r, username, sitePrefix)
	case "createpage":
		nbrew.createpage(w, r, username, sitePrefix)
	case "createfile":
	case "cut":
	case "copy":
	case "paste":
	case "rename":
	default:
		notFound(w, r)
	}
}
