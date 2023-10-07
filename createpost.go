package nb7

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

func (nbrew *Notebrew) createpost(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		Slug     string `json:"slug,omitempty"`
		Category string `json:"category,omitempty"`
		Content  string `json:"content,omitempty"`
	}
	type Response struct {
		Status         Error              `json:"status"`
		ContentSiteURL string             `json:"contentSiteURL,omitempty"`
		Name           string             `json:"name,omitempty"`
		Category       string             `json:"category,omitempty"`
		Content        string             `json:"content,omitempty"`
		Categories     []string           `json:"categories,omitempty"`
		Errors         map[string][]Error `json:"errors,omitempty"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20 /* 2MB */)
	switch r.Method {
	case "GET":
		writeResponse := func(w http.ResponseWriter, r *http.Request, response Response) {
			response.ContentSiteURL = contentSiteURL(nbrew, sitePrefix)
			dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, "posts"))
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			response.Categories = response.Categories[:0]
			for _, dirEntry := range dirEntries {
				if !dirEntry.IsDir() {
					continue
				}
				category := dirEntry.Name()
				if category != urlSafe(category) {
					continue
				}
				response.Categories = append(response.Categories, category)
			}
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
			funcMap := map[string]any{
				"join":       path.Join,
				"neatenURL":  neatenURL,
				"referer":    func() string { return r.Referer() },
				"username":   func() string { return username },
				"sitePrefix": func() string { return sitePrefix },
				"safeHTML":   func(s string) template.HTML { return template.HTML(s) },
			}
			tmpl, err := template.New("createpost.html").Funcs(funcMap).ParseFS(rootFS, "embed/createpost.html")
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			executeTemplate(w, r, time.Time{}, tmpl, &response)
		}

		err := r.ParseForm()
		if err != nil {
			badRequest(w, r, err)
			return
		}
		var response Response
		_, err = nbrew.getSession(r, "flash", &response)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		nbrew.clearSession(w, r, "flash")
		if response.Status != "" {
			writeResponse(w, r, response)
			return
		}
		response.Category = r.Form.Get("category")
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
			if !response.Status.Success() {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				var query string
				if response.Category != "" {
					query = "?category=" + url.QueryEscape(response.Category)
				}
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, "createpost")+"/"+query, http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"status": Error(fmt.Sprintf(
					`%s Created post <a href="%s" class="linktext">%s</a>`,
					response.Status.Code(),
					nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, "posts", response.Category, response.Name+".md"),
					response.Name+".md",
				)),
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, "posts", response.Category)+"/", http.StatusFound)
		}

		var request Request
		contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch contentType {
		case "application/json":
			err := json.NewDecoder(r.Body).Decode(&request)
			if err != nil {
				badRequest(w, r, err)
				return
			}
		case "application/x-www-form-urlencoded", "multipart/form-data":
			if contentType == "multipart/form-data" {
				err := r.ParseMultipartForm(2 << 20 /* 2MB */)
				if err != nil {
					badRequest(w, r, err)
					return
				}
			} else {
				err := r.ParseForm()
				if err != nil {
					badRequest(w, r, err)
					return
				}
			}
			request.Slug = r.Form.Get("slug")
			request.Category = r.Form.Get("category")
			request.Content = r.Form.Get("content")
		default:
			unsupportedContentType(w, r)
			return
		}

		response := Response{
			Name:     urlSafe(request.Slug),
			Category: request.Category,
			Content:  request.Content,
			Errors:   make(map[string][]Error),
		}
		if response.Category != "" {
			fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, "posts", response.Category))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if fileInfo == nil {
				response.Errors["category"] = append(response.Errors["category"], ErrInvalidValue)
				response.Status = ErrValidationFailed
				writeResponse(w, r, response)
				return
			}
		}
		var title string
		str := request.Content
		for str != "" {
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
		var slug string
		if request.Slug != "" {
			slug = urlSafe(request.Slug)
		} else {
			slug = urlSafe(title)
		}
		var timestamp [8]byte
		now := time.Now()
		binary.BigEndian.PutUint64(timestamp[:], uint64(now.Unix()))
		prefix := strings.TrimLeft(base32Encoding.EncodeToString(timestamp[len(timestamp)-5:]), "0")
		if slug != "" {
			response.Name = prefix + "-" + slug
		} else {
			response.Name = prefix
		}

		var rollbackItems []string
		rollback := func() {
			for _, item := range rollbackItems {
				err := RemoveAll(nbrew.FS, item)
				if err != nil {
					getLogger(r.Context()).Error(fmt.Sprintf("rollback: %v", err))
				}
			}
		}

		outputPath := path.Join(sitePrefix, "posts", response.Category, response.Name+".md")
		readerFrom, err := nbrew.FS.OpenReaderFrom(outputPath, 0644)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		_, err = readerFrom.ReadFrom(strings.NewReader(response.Content))
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		rollbackItems = append(rollbackItems, outputPath)
		templateParser := NewTemplateParser(r.Context(), nbrew, sitePrefix)

		file, err := nbrew.FS.Open(path.Join(sitePrefix, "output/themes/post.html"))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				rollback()
				return
			}
			file, err = rootFS.Open("static/post.html")
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				rollback()
				return
			}
		}
		fileInfo, err := file.Stat()
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		var b strings.Builder
		b.Grow(int(fileInfo.Size()))
		_, err = io.Copy(&b, file)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		tmpl, err := templateParser.Parse("post.html", b.String())
		if err != nil {
			var templateErrors TemplateErrors
			if errors.As(err, &templateErrors) {
				for _, msg := range templateErrors.List() {
					response.Errors["content"] = append(response.Errors["content"], Error(msg))
				}
				response.Status = ErrFileGenerationFailed
				writeResponse(w, r, response)
				rollback()
				return
			}
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		err = goldmarkMarkdown.Convert([]byte(response.Content), buf)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		content := template.HTML(buf.String())
		buf.Reset()
		err = tmpl.ExecuteTemplate(buf, "post.html", Post{
			Title:        title,
			Content:      content,
			CreationDate: now,
			LastModified: now,
		})
		if err != nil {
			response.Errors["content"] = append(response.Errors["content"], Error(err.Error()))
			response.Status = ErrFileGenerationFailed
			writeResponse(w, r, response)
			rollback()
			return
		}
		outputPath = path.Join(sitePrefix, "output/posts", response.Category, response.Name, "index.html")
		err = MkdirAll(nbrew.FS, path.Dir(outputPath), 0755)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		readerFrom, err = nbrew.FS.OpenReaderFrom(outputPath, 0644)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		_, err = readerFrom.ReadFrom(buf)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		rollbackItems = append(rollbackItems, path.Dir(outputPath))

		file, err = nbrew.FS.Open(path.Join(sitePrefix, "output/themes/posts.html"))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				rollback()
				return
			}
			file, err = rootFS.Open("static/posts.html")
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				rollback()
				return
			}
		}
		fileInfo, err = file.Stat()
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		b.Reset()
		b.Grow(int(fileInfo.Size()))
		_, err = io.Copy(&b, file)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		tmpl, err = templateParser.Parse("posts.html", b.String())
		if err != nil {
			var templateErrors TemplateErrors
			if errors.As(err, &templateErrors) {
				for _, msg := range templateErrors.List() {
					response.Errors["content"] = append(response.Errors["content"], Error(msg))
				}
				response.Status = ErrFileGenerationFailed
				writeResponse(w, r, response)
				rollback()
				return
			}
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		posts, err := nbrew.getPosts(r.Context(), sitePrefix, "")
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		buf.Reset()
		err = tmpl.ExecuteTemplate(buf, "posts.html", struct {
			Posts []Post
		}{
			Posts: posts,
		})
		if err != nil {
			response.Errors["content"] = append(response.Errors["content"], Error(err.Error()))
			response.Status = ErrFileGenerationFailed
			writeResponse(w, r, response)
			rollback()
			return
		}
		outputPath = path.Join(sitePrefix, "output/posts/index.html")
		err = MkdirAll(nbrew.FS, path.Dir(outputPath), 0755)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		readerFrom, err = nbrew.FS.OpenReaderFrom(outputPath, 0644)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}
		_, err = readerFrom.ReadFrom(buf)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			rollback()
			return
		}

		response.Status = CreatePostSuccess
		writeResponse(w, r, response)
	default:
		methodNotAllowed(w, r)
	}
}
