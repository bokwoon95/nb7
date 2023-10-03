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
	"time"
)

func (nbrew *Notebrew) createcategory(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		Type     string `json:"type,omitempty"`
		Category string `json:"category,omitempty"`
	}
	type Response struct {
		Status           Error              `json:"status"`
		ContentSiteURL   string             `json:"contentSiteURL,omitempty"`
		Type             string             `json:"type,omitempty"`
		Category         string             `json:"category,omitempty"`
		ValidationErrors map[string][]Error `json:"validationErrors,omitempty"`
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
			tmpl, err := template.New("createcategory.html").Funcs(funcMap).ParseFS(rootFS, "embed/createcategory.html")
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
		response.ValidationErrors = make(map[string][]Error)
		response.Type = r.Form.Get("type")
		switch response.Type {
		case "note", "post":
			break
		default:
			response.ValidationErrors["type"] = append(response.ValidationErrors["type"], ErrInvalidValue)
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
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, "createcategory")+"/?type="+url.QueryEscape(response.Type), http.StatusFound)
				return
			}
			var action, resource string
			switch response.Type {
			case "note":
				action, resource = "createnote", "notes"
			case "post":
				action, resource = "createpost", "posts"
			default:
				panic("unreachable")
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"status": fmt.Sprintf(
					`%s Created category <a href="%s" class="linktext">%s</a>`,
					response.Status.Code(),
					nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, resource, response.Category)+"/",
					response.Category,
				),
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, action)+"/?category="+url.QueryEscape(response.Category), http.StatusFound)
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
			request.Type = r.Form.Get("type")
			request.Category = r.Form.Get("category")
		default:
			unsupportedContentType(w, r)
			return
		}

		response := Response{
			Type:             request.Type,
			Category:         urlSafe(request.Category),
			ValidationErrors: make(map[string][]Error),
		}
		if response.Category == "" {
			response.ValidationErrors["category"] = append(response.ValidationErrors["category"], ErrFieldRequired)
		}
		var resource string
		switch response.Type {
		case "":
			response.ValidationErrors["type"] = append(response.ValidationErrors["type"], ErrFieldRequired)
		case "note":
			resource = "notes"
		case "post":
			resource = "posts"
		default:
			response.ValidationErrors["type"] = append(response.ValidationErrors["type"], ErrInvalidValue)
		}
		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, resource, response.Category))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if fileInfo != nil {
			response.ValidationErrors["category"] = append(response.ValidationErrors["category"], ErrItemAlreadyExists)
		}
		if len(response.ValidationErrors) > 0 {
			response.Status = ErrValidationFailed
			writeResponse(w, r, response)
			return
		}

		err = nbrew.FS.Mkdir(path.Join(sitePrefix, resource, response.Category), 0755)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				response.ValidationErrors["category"] = append(response.ValidationErrors["category"], ErrItemAlreadyExists)
				response.Status = ErrValidationFailed
				writeResponse(w, r, response)
				return
			}
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		response.Status = CreateCategorySuccess
		writeResponse(w, r, response)
	default:
		methodNotAllowed(w, r)
	}
}
