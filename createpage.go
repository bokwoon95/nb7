package nb7

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

func (nbrew *Notebrew) createpage(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		ParentFolder string `json:"parentFolder,omitEmpty"`
		Name         string `json:"name,omitempty"`
		Content      string `json:"content,omitempty"`
	}
	type Response struct {
		Status         Error    `json:"status"`
		ParentFolder   string   `json:"parentFolder,omitEmpty"`
		Name           string   `json:"name,omitempty"`
		Content        string   `json:"content,omitempty"`
		TemplateErrors []string `json:"templateError,omitEmpty"`
	}

	isValidParentFolder := func(parentFolder string) bool {
		segments := strings.Split(parentFolder, "/")
		if segments[0] != "pages" {
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
			var status, redirectURL string
			switch response.Status {
			case ErrParentFolderNotProvided, ErrInvalidParentFolder:
				status = response.Status.Code() + " Couldn't create item, " + response.Status.Message()
				redirectURL = nbrew.Scheme + nbrew.AdminDomain + "/" + path.Join("admin", sitePrefix) + "/"
			case ErrForbiddenName:
				status = fmt.Sprintf("%s: %s", ErrForbiddenName, response.Name)
				redirectURL = nbrew.Scheme + nbrew.AdminDomain + "/" + path.Join("admin", sitePrefix, response.ParentFolder) + "/"
			case CreateFolderSuccess:
				status = fmt.Sprintf(
					`%s Created page <a href="%s" class="linktext">%s</a>`,
					response.Status.Code(),
					"/"+path.Join("admin", sitePrefix, response.ParentFolder, response.Name)+"/",
					response.Name,
				)
				redirectURL = nbrew.Scheme + nbrew.AdminDomain + "/" + path.Join("admin", sitePrefix, response.ParentFolder) + "/"
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"status": status,
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			http.Redirect(w, r, redirectURL, http.StatusFound)
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

		var response Response
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
		response.Name = urlSafe(request.Name)
		if response.ParentFolder == "pages" && (response.Name == "admin" || response.Name == "images" || response.Name == "posts" || response.Name == "themes") {
			response.Status = Error(fmt.Sprintf("%s %q", ErrForbiddenName, request.Name))
			writeResponse(w, r, response)
			return
		}
		tmpl, templateErrors, err := nbrew.parseTemplate(sitePrefix, "", request.Content)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if len(templateErrors) > 0 {
			response.Content = request.Content
			response.TemplateErrors = templateErrors
			response.Status = ErrTemplateErrors
			writeResponse(w, r, response)
			return
		}
		_ = tmpl
		// TODO: render tmpl into path.Join(sitePrefix, "public", strings.TrimPrefix(strings.Trim(response.ParentFolder, "/"), "pages"), response.Name, "index.html.bak")
		// If there's any error, delete the index.html.bak file and append the error to templateError, status = ErrTemplateErrors and writeResponse
		// If there's no error, rename the index.html.bak file into index.html, status = CreatePageSuccess and writeResponse
	default:
		methodNotAllowed(w, r)
	}
}
