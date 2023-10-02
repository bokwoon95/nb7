package nb7

import (
	"bytes"
	"database/sql"
	"encoding/json"
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

	var typ string
	ext := path.Ext(filePath)
	switch ext {
	case ".html", ".css", ".js", ".md", ".txt", ".csv", ".tsv", ".json", ".xml", ".toml", ".yaml", ".yml", ".svg":
		typ = "text"
	case ".ico", ".jpeg", ".jpg", ".png", ".gif":
		typ = "image"
	case ".eot", ".otf", ".ttf", ".woff", ".woff2":
		typ = "font"
	case ".gz":
		typ = "gzip"
	}

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
		switch response.Type {
		case "image", "font", "gzip":
			response.Location = nbrew.Scheme + nbrew.AdminDomain + "/" + path.Join("admin", sitePrefix, tail)
		case "text":
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
		default:
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

		head, tail, _ := strings.Cut(filePath, "/")

		// If it's a note, just write it into admin/notes/*

		// If it's a post, render post to output/posts/*/tmp.html then if it passes rename the tmp.html into index.html and write the content into admin/posts/*

		// If it's a page, render page to output/*/tmp.html then if it passes rename tmp.html into index.html and write the content into admin/pages/*
		if head == "pages" {
			tmpl, tmplErrs, err := nbrew.parseTemplate(sitePrefix, "", request.Content)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if len(tmplErrs) > 0 {
				response.TemplateErrors = tmplErrs
				response.Status = ErrTemplateError
				writeResponse(w, r, response)
				return
			}
			buf := bufPool.Get().(*bytes.Buffer)
			buf.Reset()
			defer bufPool.Put(buf)
			err = tmpl.ExecuteTemplate(buf, "", nil)
			if err != nil {
				response.TemplateErrors = []string{err.Error()}
				response.Status = ErrTemplateError
				writeResponse(w, r, response)
				return
			}
			var name string
			if response.Path != "pages/index.html" {
				name = strings.TrimSuffix(tail, path.Ext(tail))
			}
			err = MkdirAll(nbrew.FS, path.Join(sitePrefix, "output", name), 0755)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, "output", name, "index.html"), 0644)
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
