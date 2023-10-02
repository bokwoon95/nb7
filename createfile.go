package nb7

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

func (nbrew *Notebrew) createfile(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		ParentFolder string `json:"parentFolder,omitempty"`
		Type         string `json:"type,omitempty"`
		Name         string `json:"name,omitempty"`
		Content      string `json:"content,omitempty"`
	}
	type Response struct {
		Status         Error    `json:"status"`
		ContentSiteURL string   `json:"contentSiteURL,omitempty"`
		ParentFolder   string   `json:"parentFolder,omitempty"`
		Type           string   `json:"type,omitempty"`
		Name           string   `json:"name,omitempty"`
		Content        string   `json:"content,omitempty"`
		TemplateErrors []string `json:"templateError,omitempty"`
	}

	isValidParentFolder := func(parentFolder string) bool {
		head, _, _ := strings.Cut(parentFolder, "/")
		if head != "public" {
			return false
		}
		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, parentFolder))
		if err != nil {
			return false
		}
		return fileInfo.IsDir()
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
			funcMap := map[string]any{
				"join":       path.Join,
				"base":       path.Base,
				"referer":    func() string { return r.Referer() },
				"username":   func() string { return username },
				"sitePrefix": func() string { return sitePrefix },
				"safeHTML":   func(s string) template.HTML { return template.HTML(s) },
				"neatenURL":  neatenURL,
			}
			tmpl, err := template.New("createfile.html").Funcs(funcMap).ParseFS(rootFS, "embed/createfile.html")
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
		}
		nbrew.clearSession(w, r, "flash")
		if response.Status != "" {
			writeResponse(w, r, response)
			return
		}
		parentFolder := r.Form.Get("parent")
		if parentFolder == "" {
			response.Status = ErrParentFolderNotProvided
			writeResponse(w, r, response)
			return
		}
		parentFolder = path.Clean(strings.Trim(parentFolder, "/"))
		if !isValidParentFolder(parentFolder) {
			response.Status = ErrInvalidParentFolder
			writeResponse(w, r, response)
			return
		}
		response.ParentFolder = parentFolder
		response.Type = r.Form.Get("type")
		switch response.Type {
		case "html", "css", "js":
			break
		default:
			response.Status = ErrInvalidType
			writeResponse(w, r, response)
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
			if !response.Status.Success() {
				if response.Status == ErrParentFolderNotProvided || response.Status == ErrInvalidParentFolder {
					err := nbrew.setSession(w, r, "flash", map[string]any{
						"status": response.Status.Code() + " Couldn't create item, " + response.Status.Message(),
					})
					if err != nil {
						getLogger(r.Context()).Error(err.Error())
						internalServerError(w, r, err)
						return
					}
					http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix)+"/", http.StatusFound)
					return
				}
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, "createfile")+"/?parent="+url.QueryEscape(response.ParentFolder)+"&type="+url.QueryEscape(response.Type), http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"status": fmt.Sprintf(
					`%s Created file <a href="%s" class="linktext">%s</a>`,
					response.Status.Code(),
					"/"+path.Join("admin", sitePrefix, response.ParentFolder, response.Name),
					response.Name,
				),
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, response.ParentFolder)+"/", http.StatusFound)
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
				err := r.ParseMultipartForm(15 << 20 /* 15MB */)
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
			request.ParentFolder = r.Form.Get("parentFolder")
			request.Type = r.Form.Get("type")
			request.Name = r.Form.Get("name")
			request.Content = r.Form.Get("content")
		default:
			unsupportedContentType(w, r)
			return
		}

		response := Response{
			Type:    request.Type,
			Content: request.Content,
		}
		if request.ParentFolder == "" {
			response.Status = ErrParentFolderNotProvided
			writeResponse(w, r, response)
			return
		}
		response.ParentFolder = path.Clean(strings.Trim(request.ParentFolder, "/"))
		if !isValidParentFolder(response.ParentFolder) {
			response.Status = ErrInvalidParentFolder
			writeResponse(w, r, response)
			return
		}
		switch response.Type {
		case "html", "css", "js":
			break
		default:
			response.Status = ErrInvalidType
			writeResponse(w, r, response)
			return
		}
		response.Name = urlSafe(request.Name)
		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, response.Name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if fileInfo != nil {
			response.Status = ErrItemAlreadyExists
			writeResponse(w, r, response)
			return
		}
		tmpl, tmplErrs, err := nbrew.parseTemplate(sitePrefix, "", request.Content)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if len(tmplErrs) > 0 {
			response.Content = request.Content
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
			response.Content = request.Content
			response.TemplateErrors = []string{err.Error()}
			response.Status = ErrTemplateError
			writeResponse(w, r, response)
			return
		}
		err = MkdirAll(nbrew.FS, path.Join(sitePrefix, "public", strings.TrimPrefix(strings.Trim(response.ParentFolder, "/"), "pages"), response.Name), 0755)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, "public", strings.TrimPrefix(strings.Trim(response.ParentFolder, "/"), "pages"), response.Name, "index.html"), 0644)
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
		readerFrom, err = nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, response.ParentFolder, response.Name+".html"), 0644)
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
		response.Status = CreatePageSuccess
		writeResponse(w, r, response)
	default:
		methodNotAllowed(w, r)
	}
}
