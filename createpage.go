package nb7

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

func (nbrew *Notebrew) createpage(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		ParentFolder string `json:"parentFolder,omitempty"`
		Name         string `json:"name,omitempty"`
		Content      string `json:"content,omitempty"`
	}
	type Response struct {
		Status         Error              `json:"status"`
		ContentSiteURL string             `json:"contentSiteURL,omitempty"`
		ParentFolder   string             `json:"parentFolder,omitempty"`
		Name           string             `json:"name,omitempty"`
		Content        string             `json:"content,omitempty"`
		Errors         map[string][]Error `json:"errors,omitempty"`
	}

	isValidParentFolder := func(parentFolder string) bool {
		head, _, _ := strings.Cut(parentFolder, "/")
		if head != "pages" {
			return false
		}
		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, parentFolder))
		if err != nil {
			return false
		}
		return fileInfo.IsDir()
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20 /* 2MB */)
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
				"neatenURL":  neatenURL,
				"referer":    func() string { return r.Referer() },
				"username":   func() string { return username },
				"sitePrefix": func() string { return sitePrefix },
				"safeHTML":   func(s string) template.HTML { return template.HTML(s) },
				"containsError": func(errors []Error, codes ...string) bool {
					return slices.ContainsFunc(errors, func(err Error) bool {
						return slices.Contains(codes, err.Code())
					})
				},
			}
			tmpl, err := template.New("createpage.html").Funcs(funcMap).ParseFS(rootFS, "embed/createpage.html")
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
		response.Errors = make(map[string][]Error)
		response.ParentFolder = r.Form.Get("parent")
		if response.ParentFolder == "" {
			response.Errors["parentFolder"] = append(response.Errors["parentFolder"], ErrFieldRequired)
		} else {
			response.ParentFolder = path.Clean(strings.Trim(response.ParentFolder, "/"))
			if !isValidParentFolder(response.ParentFolder) {
				response.Errors["parentFolder"] = append(response.Errors["parentFolder"], ErrInvalidValue)
			}
		}
		if len(response.Errors) > 0 {
			response.Status = ErrValidationFailed
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
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				if response.Status == ErrFileGenerationFailed {
					http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, response.ParentFolder, response.Name+".html"), http.StatusFound)
					return
				}
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, "createpage")+"/?parent="+url.QueryEscape(response.ParentFolder), http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"status": fmt.Sprintf(
					`%s Created page <a href="%s" class="linktext">%s</a>`,
					response.Status.Code(),
					nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, response.ParentFolder, response.Name+".html"),
					response.Name+".html",
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
			request.ParentFolder = r.Form.Get("parentFolder")
			request.Name = r.Form.Get("name")
			request.Content = r.Form.Get("content")
		default:
			unsupportedContentType(w, r)
			return
		}

		response := Response{
			ParentFolder: request.ParentFolder,
			Name:         urlSafe(request.Name),
			Content:      request.Content,
			Errors:       make(map[string][]Error),
		}
		if response.ParentFolder == "" {
			response.Errors["parentFolder"] = append(response.Errors["parentFolder"], ErrFieldRequired)
		} else {
			response.ParentFolder = path.Clean(strings.Trim(response.ParentFolder, "/"))
			if !isValidParentFolder(response.ParentFolder) {
				response.Errors["parentFolder"] = append(response.Errors["parentFolder"], ErrInvalidValue)
			}
		}
		if response.Name == "" {
			response.Errors["name"] = append(response.Errors["name"], ErrFieldRequired)
		} else {
			if response.ParentFolder == "pages" {
				switch response.Name {
				case "admin", "forum", "images", "media", "posts", "status", "themes", "thread", "user":
					response.Errors["name"] = append(response.Errors["name"], ErrForbiddenValue)
				}
			}
		}
		if len(response.Errors) > 0 {
			response.Status = ErrValidationFailed
			writeResponse(w, r, response)
			return
		}

		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, response.Name+".html"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if fileInfo != nil {
			response.Errors["name"] = append(response.Errors["name"], ErrItemAlreadyExists)
			response.Status = ErrValidationFailed
			writeResponse(w, r, response)
			return
		}

		readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, response.ParentFolder, response.Name+".html"), 0644)
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

		err = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Minute))
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		err = nbrew.RegenerateSite(r.Context(), sitePrefix)
		if err != nil {
			var templateError TemplateError
			if errors.As(err, &templateError) {
				for _, msg := range templateError.ToList() {
					response.Errors["content"] = append(response.Errors["content"], Error(msg))
				}
				response.Status = ErrFileGenerationFailed
				writeResponse(w, r, response)
				return
			}
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
