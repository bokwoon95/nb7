package nb7

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

func (nbrew *Notebrew) file(w http.ResponseWriter, r *http.Request, username, sitePrefix, filePath string, fileInfo fs.FileInfo) {
	type Request struct {
		Content string `json:"content"`
	}
	type Response struct {
		Status   Error      `json:"status"`
		Path     string     `json:"path"`
		IsDir    bool       `json:"isDir,omitempty"`
		ModTime  *time.Time `json:"modTime,omitempty"`
		Type     string     `json:"type,omitempty"`
		Content  string     `json:"content,omitempty"`
		Location string     `json:"location,omitempty"`
		Errors   []string   `json:"errors,omitempty"`
	}

	var typ string
	head, tail, _ := strings.Cut(filePath, "/")
	ext := path.Ext(filePath)
	switch head {
	case "notes", "posts":
		if strings.Count(filePath, "/") > 2 {
			// Return 404 for files that are more than 1 folder deep inside
			// notes or posts.
			//
			// (ok)     notes/file.md
			// (ok)     notes/foo/file.md
			// (not ok) notes/foo/bar/file.md
			notFound(w, r)
			return
		}
		if ext != ".md" && ext != ".txt" {
			notFound(w, r)
			return
		}
		typ = "text"
	case "pages":
		if ext != ".html" {
			notFound(w, r)
			return
		}
		typ = "text"
	case "public":
		next, _, _ := strings.Cut(tail, "/")
		if next != "images" && next != "themes" {
			notFound(w, r)
			return
		}
		switch ext {
		case ".html", ".css", ".js", ".md", ".txt", ".csv", ".tsv", ".json", ".xml", ".toml", ".yaml", ".yml", ".svg":
			typ = "text"
		case ".ico", ".jpeg", ".jpg", ".png", ".gif":
			typ = "image"
		case ".eot", ".otf", ".ttf", ".woff", ".woff2":
			typ = "font"
		case ".gz":
			typ = "gzip"
		default:
			notFound(w, r)
			return
		}
		//
	default:
		notFound(w, r)
		return
	}

	switch r.Method {
	case "GET":
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
				"join":       path.Join,
				"dir":        path.Dir,
				"base":       path.Base,
				"referer":    func() string { return r.Referer() },
				"username":   func() string { return username },
				"sitePrefix": func() string { return sitePrefix },
				"title":      func() string { return title },
				"safeHTML":   func(s string) template.HTML { return template.HTML(s) },
				"head": func(s string) string {
					head, _, _ := strings.Cut(s, "/")
					return head
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

		var response Response
		_, err = nbrew.getSession(r, "flash", &response)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
		}
		nbrew.clearSession(w, r, "flash")
		if response.Status.Equal("") {
			response.Status = Success
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
			return
		}
		_ = writeResponse

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
				var syntaxErr *json.SyntaxError
				if err == io.EOF || err == io.ErrUnexpectedEOF || errors.As(err, &syntaxErr) {
					badRequest(w, r, err)
					return
				}
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		case "application/x-www-form-urlencoded", "multipart/form-data":
			if contentType == "multipart/form-data" {
				err := r.ParseMultipartForm(5 << 20 /* 5MB */)
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
		// NOTE: application/x-www-form-urlencoded, multipart/form-data and
		// application/json are accepted but if the type is not "text" then
		// anything other than multipart/form-data will result in
		// unsupportedContentType(w, r).
		methodNotAllowed(w, r)
	default:
		methodNotAllowed(w, r)
	}
}
