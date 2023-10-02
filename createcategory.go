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
	"time"
)

func (nbrew *Notebrew) createcategory(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		Type     string `json:"type,omitempty"`
		Category string `json:"category,omitempty"`
	}
	type Response struct {
		Status   Error  `json:"status"`
		Type     string `json:"type,omitempty"`
		Category string `json:"category,omitempty"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20 /* 2MB */)
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
				"referer":    func() string { return r.Referer() },
				"username":   func() string { return username },
				"sitePrefix": func() string { return sitePrefix },
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
		response.Type = r.Form.Get("type")
		switch response.Type {
		case "note", "post":
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
			var status string
			switch response.Status {
			case ErrItemAlreadyExists:
				status = fmt.Sprintf("%s Category %s already exists", response.Status.Code(), response.Category)
			case CreateCategorySuccess:
				status = fmt.Sprintf("%s Created category %s", response.Status.Code(), response.Category)
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"status": status,
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			var action string
			switch response.Type {
			case "note":
				action = "createnote"
			case "post":
				action = "createpost"
			default:
				panic("unreachable")
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
			Type:     request.Type,
			Category: urlSafe(request.Category),
		}

		var resource string
		switch response.Type {
		case "note":
			resource = "notes"
		case "post":
			resource = "posts"
		default:
			response.Status = ErrInvalidType
			writeResponse(w, r, response)
			return
		}

		err := nbrew.FS.Mkdir(path.Join(sitePrefix, resource, response.Category), 0755)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				response.Status = ErrItemAlreadyExists
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
