package nb7

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

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

	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if segments[0] == "admin" {
		switch strings.Trim(r.URL.Path, "/") {
		case "app.webmanifest":
			nbrew.securityHeaders(w, r)
			serveFile(w, r, rootFS, "static/app.webmanifest", false)
			return
		case "apple-touch-icon.png":
			nbrew.securityHeaders(w, r)
			serveFile(w, r, rootFS, "static/icons/apple-touch-icon.png", false)
			return
		}
	}

	if len(segments) < 2 || segments[0] != "admin" || segments[1] != "static" {
		file, err := nbrew.FS.Open("config/show-latency.txt")
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error(err.Error())
		} else {
			defer file.Close()
			reader := bufio.NewReader(file)
			b, _ := reader.Peek(6)
			if len(b) > 0 {
				ok, _ := strconv.ParseBool(string(bytes.TrimSpace(b)))
				if ok {
					startedAt := time.Now()
					defer func() {
						timeTaken := time.Since(startedAt)
						fmt.Printf("%s %s %s\n", r.Method, r.URL.RequestURI(), timeTaken.String())
					}()
				}
			}
		}
	}

	host := getHost(r)
	urlPath := strings.Trim(r.URL.Path, "/")
	head, tail, _ := strings.Cut(urlPath, "/")
	if (host == nbrew.AdminDomain || host == "www."+nbrew.AdminDomain) && head == "admin" {
		nbrew.securityHeaders(w, r)
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
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}
	if sitePrefix != "" {
		urlPath = tail
		siteName := strings.TrimPrefix(sitePrefix, "@")
		for _, char := range siteName {
			if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '-' {
				continue
			}
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		if siteName == "www" {
			sitePrefix = ""
		} else if nbrew.MultisiteMode == "subdomain" {
			http.Redirect(w, r, nbrew.Scheme+siteName+"."+nbrew.ContentDomain+"/"+urlPath, http.StatusFound)
			return
		}
	} else if subdomainPrefix != "" {
		sitePrefix = "@" + subdomainPrefix
		for _, char := range subdomainPrefix {
			if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '-' {
				continue
			}
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
		if subdomainPrefix == "www" {
			sitePrefix = ""
		} else if nbrew.MultisiteMode == "subdirectory" {
			http.Redirect(w, r, nbrew.Scheme+nbrew.ContentDomain+"/"+path.Join(sitePrefix, urlPath), http.StatusFound)
			return
		}
	} else if customDomain != "" {
		sitePrefix = customDomain
		fileInfo, err := fs.Stat(nbrew.FS, customDomain)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "404 Not Found", http.StatusNotFound)
				return
			}
			logger.Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if !fileInfo.IsDir() {
			http.Error(w, "404 Not Found", http.StatusNotFound)
			return
		}
	}
	if nbrew.MultisiteMode == "" && sitePrefix != "" {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}

	custom404 := func(w http.ResponseWriter, r *http.Request, sitePrefix string) {
		file, err := nbrew.FS.Open(path.Join(sitePrefix, "output/themes/404.html"))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "404 Not Found", http.StatusNotFound)
				return
			}
			getLogger(r.Context()).Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		fileInfo, err := file.Stat()
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		var b strings.Builder
		b.Grow(int(fileInfo.Size()))
		_, err = io.Copy(&b, file)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		templateParser, err := NewTemplateParser(r.Context(), nbrew, sitePrefix)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		tmpl, err := templateParser.Parse(b.String())
		if err != nil {
			http.Error(w, "404 Not Found\n"+err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		err = tmpl.Execute(w, nil)
		if err != nil {
			io.WriteString(w, err.Error())
		}
	}

	if r.Method != "GET" {
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	name := path.Join(sitePrefix, "output", urlPath)
	ext := path.Ext(name)
	if ext == "" {
		name = name + "/index.html"
		ext = ".html"
	}
	extInfo, ok := extensionInfo[ext]
	if !ok {
		custom404(w, r, sitePrefix)
		return
	}

	var isGzipped bool
	file, err := nbrew.FS.Open(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			getLogger(r.Context()).Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		if !extInfo.isGzippable {
			custom404(w, r, sitePrefix)
			return
		}
		file, err = nbrew.FS.Open(name + ".gz")
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				getLogger(r.Context()).Error(err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			custom404(w, r, sitePrefix)
			return
		}
		isGzipped = true
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		getLogger(r.Context()).Error(err.Error())
		http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		return
	}
	if fileInfo.IsDir() {
		custom404(w, r, sitePrefix)
		return
	}

	if !extInfo.isGzippable {
		fileSeeker, ok := file.(io.ReadSeeker)
		if ok {
			http.ServeContent(w, r, name, fileInfo.ModTime(), fileSeeker)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		_, err = buf.ReadFrom(file)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, name, fileInfo.ModTime(), bytes.NewReader(buf.Bytes()))
		return
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	hasher := hashPool.Get().(hash.Hash)
	hasher.Reset()
	defer hashPool.Put(hasher)

	multiWriter := io.MultiWriter(buf, hasher)
	if isGzipped {
		_, err = io.Copy(multiWriter, file)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
	} else {
		gzipWriter := gzipPool.Get().(*gzip.Writer)
		gzipWriter.Reset(multiWriter)
		defer gzipPool.Put(gzipWriter)
		_, err = io.Copy(gzipWriter, file)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
		err = gzipWriter.Close()
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	src := bytesPool.Get().(*[]byte)
	*src = (*src)[:0]
	defer bytesPool.Put(src)

	dst := bytesPool.Get().(*[]byte)
	*dst = (*dst)[:0]
	defer bytesPool.Put(dst)

	*src = hasher.Sum(*src)
	encodedLen := hex.EncodedLen(len(*src))
	if cap(*dst) < encodedLen {
		*dst = make([]byte, encodedLen)
	}
	*dst = (*dst)[:encodedLen]
	hex.Encode(*dst, *src)

	w.Header().Set("Content-Type", extInfo.contentType)
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("ETag", `"`+string(*dst)+`"`)
	http.ServeContent(w, r, name, fileInfo.ModTime(), bytes.NewReader(buf.Bytes()))
}

func (nbrew *Notebrew) securityHeaders(w http.ResponseWriter, r *http.Request) {
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
}

var extensionInfo = map[string]struct {
	contentType string
	isGzippable bool
}{
	".html":  {"text/html", true},
	".css":   {"text/css", true},
	".js":    {"text/javascript", true},
	".md":    {"text/markdown", true},
	".txt":   {"text/plain", true},
	".csv":   {"text/csv", true},
	".tsv":   {"text/tsv", true},
	".json":  {"application/json", true},
	".xml":   {"application/xml", true},
	".toml":  {"application/toml", true},
	".yaml":  {"application/yaml", true},
	".svg":   {"image/svg", true},
	".ico":   {"image/ico", true},
	".jpeg":  {"image/jpeg", false},
	".jpg":   {"image/jpeg", false},
	".png":   {"image/png", false},
	".gif":   {"image/gif", false},
	".eot":   {"font/eot", false},
	".otf":   {"font/otf", false},
	".ttf":   {"font/ttf", false},
	".woff":  {"font/woff", false},
	".woff2": {"font/woff2", false},
	".gzip":  {"application/gzip", false},
	".gz":    {"application/gzip", false},
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
		serveFile(w, r, nbrew.FS, path.Join(sitePrefix, "output", urlPath), true)
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
		if !result.IsAuthorized {
			if (sitePrefix != "" || head != "") && head != "createsite" && head != "deletesite" {
				notAuthorized(w, r)
				return
			}
		}
	}

	if head == "" || head == "notes" || head == "output" || head == "pages" || head == "posts" {
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
		nbrew.createfile(w, r, username, sitePrefix)
	case "cut":
	case "copy":
	case "paste":
	case "rename":
	default:
		notFound(w, r)
	}
}
