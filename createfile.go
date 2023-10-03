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
	"slices"
	"strings"
	"time"
)

func (nbrew *Notebrew) createfile(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		ParentFolder string `json:"parentFolder,omitempty"`
		Ext          string `json:"ext,omitempty"`
		Name         string `json:"name,omitempty"`
		Content      string `json:"content,omitempty"`
	}
	type Response struct {
		Status           Error              `json:"status"`
		ContentSiteURL   string             `json:"contentSiteURL,omitempty"`
		ParentFolder     string             `json:"parentFolder,omitempty"`
		Ext              string             `json:"ext,omitempty"`
		Name             string             `json:"name,omitempty"`
		Content          string             `json:"content,omitempty"`
		ValidationErrors map[string][]Error `json:"validationErrors,omitempty"`
	}

	isValidParentFolder := func(parentFolder string) bool {
		head, _, _ := strings.Cut(parentFolder, "/")
		if head != "output" {
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
				"join":          path.Join,
				"base":          path.Base,
				"neatenURL":     neatenURL,
				"templateError": templateError,
				"referer":       func() string { return r.Referer() },
				"username":      func() string { return username },
				"sitePrefix":    func() string { return sitePrefix },
				"safeHTML":      func(s string) template.HTML { return template.HTML(s) },
				"containsError": func(errors []Error, codes ...string) bool {
					return slices.ContainsFunc(errors, func(err Error) bool {
						return slices.Contains(codes, err.Code())
					})
				},
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
		response.ValidationErrors = make(map[string][]Error)
		response.ParentFolder = r.Form.Get("parent")
		if response.ParentFolder == "" {
			response.ValidationErrors["parentFolder"] = append(response.ValidationErrors["parentFolder"], ErrFieldRequired)
		} else {
			response.ParentFolder = path.Clean(strings.Trim(response.ParentFolder, "/"))
			if !isValidParentFolder(response.ParentFolder) {
				response.ValidationErrors["parentFolder"] = append(response.ValidationErrors["parentFolder"], ErrInvalidValue)
			}
		}
		response.Ext = r.Form.Get("ext")
		switch response.Ext {
		case "":
			response.ValidationErrors["ext"] = append(response.ValidationErrors["ext"], ErrFieldRequired)
		case "html", "css", "js":
			break
		default:
			response.ValidationErrors["ext"] = append(response.ValidationErrors["ext"], ErrInvalidValue)
		}
		if len(response.ValidationErrors) > 0 {
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
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, "createfile")+"/?parent="+url.QueryEscape(response.ParentFolder)+"&ext="+url.QueryEscape(response.Ext), http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"status": fmt.Sprintf(
					`%s Created file <a href="%s" class="linktext">%s</a>`,
					response.Status.Code(),
					nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, response.ParentFolder, response.Name+"."+response.Ext),
					response.Name+"."+response.Ext,
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
			request.Ext = r.Form.Get("ext")
			request.Name = r.Form.Get("name")
			request.Content = r.Form.Get("content")
		default:
			unsupportedContentType(w, r)
			return
		}

		response := Response{
			ParentFolder:     request.ParentFolder,
			Ext:              request.Ext,
			Name:             urlSafe(request.Name),
			Content:          request.Content,
			ValidationErrors: make(map[string][]Error),
		}
		if response.ParentFolder == "" {
			response.ValidationErrors["parentFolder"] = append(response.ValidationErrors["parentFolder"], ErrFieldRequired)
		} else {
			response.ParentFolder = path.Clean(strings.Trim(response.ParentFolder, "/"))
			if !isValidParentFolder(response.ParentFolder) {
				response.ValidationErrors["parentFolder"] = append(response.ValidationErrors["parentFolder"], ErrInvalidValue)
			}
		}
		switch response.Ext {
		case "":
			response.ValidationErrors["ext"] = append(response.ValidationErrors["ext"], ErrFieldRequired)
		case "html", "css", "js":
			break
		default:
			response.ValidationErrors["ext"] = append(response.ValidationErrors["ext"], ErrInvalidValue)
		}
		if len(response.ValidationErrors) > 0 {
			response.Status = ErrValidationFailed
			writeResponse(w, r, response)
			return
		}

		fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, response.Name+"."+response.Ext))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if fileInfo != nil {
			response.ValidationErrors["name"] = append(response.ValidationErrors["name"], ErrItemAlreadyExists)
			response.Status = ErrValidationFailed
			writeResponse(w, r, response)
			return
		}

		if response.Ext == "html" {
			tmpl, tmplErrs, err := nbrew.parseTemplate(sitePrefix, "", request.Content, nil)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if len(tmplErrs) > 0 {
				response.ValidationErrors["content"] = tmplErrs
				response.Status = ErrValidationFailed
				writeResponse(w, r, response)
				return
			}
			buf := bufPool.Get().(*bytes.Buffer)
			buf.Reset()
			defer bufPool.Put(buf)
			err = tmpl.ExecuteTemplate(buf, "", nil)
			if err != nil {
				response.ValidationErrors["content"] = append(response.ValidationErrors["content"], Error(err.Error()))
				response.Status = ErrValidationFailed
				writeResponse(w, r, response)
				return
			}
		}

		readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, response.ParentFolder, response.Name+"."+response.Ext), 0644)
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
		response.Status = CreateFileSuccess
		writeResponse(w, r, response)
	default:
		methodNotAllowed(w, r)
	}
}
