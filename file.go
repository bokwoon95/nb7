package nb7

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/bokwoon95/sq"
)

var extTypes = map[string]string{
	".html":  "text/html",
	".css":   "text/css",
	".js":    "text/javascript",
	".md":    "text/markdown",
	".txt":   "text/plain",
	".csv":   "text/csv",
	".tsv":   "text/tsv",
	".json":  "text/json",
	".xml":   "text/xml",
	".toml":  "text/toml",
	".yaml":  "text/yaml",
	".svg":   "image/svg",
	".ico":   "image/ico",
	".jpeg":  "image/jpeg",
	".jpg":   "image/jpeg",
	".png":   "image/png",
	".gif":   "image/gif",
	".eot":   "font/eot",
	".otf":   "font/otf",
	".ttf":   "font/ttf",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".gzip":  "gzip",
}

func (nbrew *Notebrew) file(w http.ResponseWriter, r *http.Request, username, sitePrefix, filePath string, fileInfo fs.FileInfo) {
	type Request struct {
		Content string `json:"content"`
	}
	type Response struct {
		Status         Error      `json:"status"`
		ContentSiteURL string     `json:"contentSiteURL,omitempty"`
		Path           string     `json:"path"`
		IsDir          bool       `json:"isDir,omitempty"`
		ModTime        *time.Time `json:"modTime,omitempty"`
		Type           string     `json:"type,omitempty"`
		Content        string     `json:"content,omitempty"`
		Location       string     `json:"location,omitempty"`
		TemplateErrors []string   `json:"templateErrors,omitempty"`
		StorageUsed    int64      `json:"storageUsed,omitempty"`
		StorageLimit   int64      `json:"storageLimit,omitempty"`
	}

	ext := path.Ext(filePath)
	typ := extTypes[ext]
	r.Body = http.MaxBytesReader(w, r.Body, 15<<20 /* 15MB */)
	switch r.Method {
	case "GET":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			response.ContentSiteURL = contentSiteURL(nbrew, sitePrefix)

			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				w.Header().Set("Content-Type", "application/json")
				encoder := json.NewEncoder(w)
				encoder.SetEscapeHTML(false)
				err := encoder.Encode(&response)
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
				}
				return
			}
			var title string
			str := response.Content
			for {
				if str == "" {
					break
				}
				title, str, _ = strings.Cut(str, "\n")
				title = strings.TrimSpace(title)
				if title == "" {
					continue
				}
				var b strings.Builder
				stripMarkdownStyles(&b, []byte(title))
				title = b.String()
				break
			}
			funcMap := map[string]any{
				"join":             path.Join,
				"dir":              path.Dir,
				"base":             path.Base,
				"neatenURL":        neatenURL,
				"fileSizeToString": fileSizeToString,
				"referer":          func() string { return r.Referer() },
				"username":         func() string { return username },
				"sitePrefix":       func() string { return sitePrefix },
				"title":            func() string { return title },
				"safeHTML":         func(s string) template.HTML { return template.HTML(s) },
				"head": func(s string) string {
					head, _, _ := strings.Cut(s, "/")
					return head
				},
				"tail": func(s string) string {
					_, tail, _ := strings.Cut(s, "/")
					return tail
				},
				"hasPrefix": func(s string, prefixes ...string) bool {
					for _, prefix := range prefixes {
						if strings.HasPrefix(s, prefix) {
							return true
						}
					}
					return false
				},
				"hasSuffix": func(s string, suffixes ...string) bool {
					for _, suffix := range suffixes {
						if strings.HasSuffix(s, suffix) {
							return true
						}
					}
					return false
				},
			}
			tmpl, err := template.New("file.html").Funcs(funcMap).ParseFS(rootFS, "embed/file.html")
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			executeTemplate(w, r, fileInfo.ModTime(), tmpl, &response)
		}
		err := r.ParseForm()
		if err != nil {
			badRequest(w, r, err)
			return
		}
		if fileInfo == nil {
			fileInfo, err = fs.Stat(nbrew.FS, path.Join(sitePrefix, filePath))
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		}

		_, tail, _ := strings.Cut(filePath, "/")

		var response Response
		_, err = nbrew.getSession(r, "flash", &response)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
		}
		nbrew.clearSession(w, r, "flash")
		if response.Status != "" {
			writeResponse(w, r, response)
			return
		}
		response.Path = filePath
		response.Type = typ
		response.IsDir = fileInfo.IsDir()
		modTime := fileInfo.ModTime()
		if !modTime.IsZero() {
			response.ModTime = &modTime
		}
		if strings.HasPrefix(response.Type, "image") || strings.HasPrefix(response.Type, "font") || response.Type == "gzip" {
			response.Location = nbrew.Scheme + nbrew.AdminDomain + "/" + path.Join("admin", sitePrefix, tail)
		} else if strings.HasPrefix(response.Type, "text") {
			file, err := nbrew.FS.Open(path.Join(sitePrefix, filePath))
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			defer file.Close()
			var b strings.Builder
			b.Grow(int(fileInfo.Size()))
			_, err = io.Copy(&b, file)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			response.Content = b.String()
		} else {
			notFound(w, r)
			return
		}
		response.Status = Success
		writeResponse(w, r, response)
	case "POST":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
			if accept == "application/json" {
				w.Header().Set("Content-Type", "application/json")
				encoder := json.NewEncoder(w)
				encoder.SetEscapeHTML(false)
				err := encoder.Encode(&response)
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
				}
				return
			}
			err := nbrew.setSession(w, r, "flash", &response)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, filePath), http.StatusFound)
		}

		var request Request
		contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch contentType {
		case "application/json":
			if typ != "text" {
				unsupportedContentType(w, r)
				return
			}
			err := json.NewDecoder(r.Body).Decode(&request)
			if err != nil {
				badRequest(w, r, err)
				return
			}
		case "application/x-www-form-urlencoded", "multipart/form-data":
			if contentType == "multipart/form-data" {
				err := r.ParseMultipartForm(15 << 20 /* 15MB */)
				if err != nil {
					badRequest(w, r, err)
					return
				}
			} else {
				if typ != "text" {
					unsupportedContentType(w, r)
					return
				}
				err := r.ParseForm()
				if err != nil {
					badRequest(w, r, err)
					return
				}
			}
			request.Content = r.Form.Get("content")
		default:
			unsupportedContentType(w, r)
			return
		}

		response := Response{
			Path:    filePath,
			IsDir:   fileInfo.IsDir(),
			Type:    typ,
			Content: request.Content,
		}
		modTime := fileInfo.ModTime()
		if !modTime.IsZero() {
			response.ModTime = &modTime
		}

		if nbrew.DB != nil {
			result, err := sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format:  "SELECT {*} FROM site WHERE site_name = {siteName}",
				Values: []any{
					sq.StringParam("siteName", strings.TrimPrefix(sitePrefix, "@")),
				},
			}, func(row *sq.Row) (result struct {
				StorageLimit sql.NullInt64
				StorageUsed  int64
			}) {
				result.StorageLimit = row.NullInt64("storage_limit")
				result.StorageUsed = row.Int64("storage_used")
				return result
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if result.StorageLimit.Valid && result.StorageUsed+int64(len(request.Content)) > result.StorageLimit.Int64 {
				response.StorageUsed = result.StorageUsed
				response.StorageLimit = result.StorageLimit.Int64
				response.Status = ErrStorageLimitExceeded
				writeResponse(w, r, response)
				return
			}
			logger := getLogger(r.Context())
			defer func() {
				storageUsed, err := getFileSize(nbrew.FS, sitePrefix)
				if err != nil {
					logger.Error(err.Error())
					return
				}
				_, err = sq.Exec(nbrew.DB, sq.CustomQuery{
					Dialect: nbrew.Dialect,
					Format:  "UPDATE site SET storage_used = {storageUsed} WHERE site_name = {siteName}",
					Values: []any{
						sq.Int64Param("storageUsed", storageUsed),
						sq.StringParam("siteName", strings.TrimPrefix(sitePrefix, "@")),
					},
				})
				if err != nil {
					logger.Error(err.Error())
					return
				}
			}()
		}

		segments := strings.Split(filePath, "/")
		if segments[0] == "posts" && len(segments) <= 3 && (ext == ".md" || ext == ".txt") {
			var category, name string
			if len(segments) == 3 {
				category, name = segments[1], segments[2]
			} else {
				name = segments[1]
			}
			var creationDate time.Time
			prefix, _, ok := strings.Cut(path.Base(filePath), "-")
			if ok && len(prefix) > 0 && len(prefix) <= 8 {
				b, _ := base32Encoding.DecodeString(fmt.Sprintf("%08s", prefix))
				if len(b) == 5 {
					var timestamp [8]byte
					copy(timestamp[len(timestamp)-5:], b)
					creationDate = time.Unix(int64(binary.BigEndian.Uint64(timestamp[:])), 0)
				}
			}
			renderer := NewRenderer(r.Context(), nbrew, sitePrefix)
			buf := bufPool.Get().(*bytes.Buffer)
			buf.Reset()
			defer bufPool.Put(buf)
			err := renderer.RenderPost(buf, []byte(response.Content), creationDate, time.Now())
			if err != nil {
				var renderError RenderError
				if errors.As(err, &renderError) {
					response.TemplateErrors = renderError.Errors()
					response.Status = ErrFileGenerationFailed
					writeResponse(w, r, response)
					return
				}
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			err = MkdirAll(nbrew.FS, path.Join(sitePrefix, "output/posts", category, name), 0755)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, "output/posts", category, name, "index.html"), 0644)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			_, err = readerFrom.ReadFrom(buf)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		} else if segments[0] == "pages" && ext == ".html" {
			renderer := NewRenderer(r.Context(), nbrew, sitePrefix)
			buf := bufPool.Get().(*bytes.Buffer)
			buf.Reset()
			defer bufPool.Put(buf)
			renderer.Render(buf, response.Content)
			tmpl, err := NewTemplateParser(r.Context(), nbrew, sitePrefix).Parse(filePath, response.Content)
			if err != nil {
				var templateErrors TemplateErrors
				if errors.As(err, &templateErrors) {
					response.TemplateErrors = templateErrors.List()
					response.Status = ErrValidationFailed
					writeResponse(w, r, response)
					return
				}
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			err = tmpl.ExecuteTemplate(buf, filePath, nil)
			if err != nil {
				response.TemplateErrors = append(response.TemplateErrors, err.Error())
				response.Status = ErrTemplateError
				writeResponse(w, r, response)
				return
			}
			var outputFilepath string
			if response.Path == "pages/index.html" {
				outputFilepath = path.Join(sitePrefix, "output", "index.html")
			} else {
				outputFilepath = path.Join(sitePrefix, "output", strings.TrimSuffix(path.Join(segments[1:]...), ext), "index.html")
			}
			err = MkdirAll(nbrew.FS, path.Dir(outputFilepath), 0755)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			readerFrom, err := nbrew.FS.OpenReaderFrom(outputFilepath, 0644)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			_, err = readerFrom.ReadFrom(buf)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		} else if len(segments) > 2 && segments[0] == "output" && segments[1] == "themes" && ext == ".html" {
			categories := []string{""}
			dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, "posts"))
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			for _, dirEntry := range dirEntries {
				if dirEntry.IsDir() {
					categories = append(categories, dirEntry.Name())
				}
			}
			// generate the posts
			// generate the pages
		}

		// If it's an image, just write the image into output/images/*

		// If it's a theme file that is not html, just write it into output/theme/*

		// If it's a theme file that is html, find all other pages that depend on this template and render them into output/posts/*/tmp.html and output/*/tmp.html and if all succeed then start renaming all those tmp.html into index.html and write the content into output/theme/*

		readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, filePath), 0644)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		_, err = readerFrom.ReadFrom(strings.NewReader(request.Content))
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		response.Status = UpdateSuccess
		writeResponse(w, r, response)
	default:
		methodNotAllowed(w, r)
	}
}
