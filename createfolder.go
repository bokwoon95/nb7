package nb7

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

func (nbrew *Notebrew) createfolder(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		ParentFolder string `json:"parentFolder,omitempty"`
		Name         string `json:"name,omitempty"`
	}
	type Response struct {
		Status       Error  `json:"status"`
		ParentFolder string `json:"parentFolder,omitempty"`
		Name         string `json:"name,omitempty"`
	}

	isValidParentFolder := func(parentFolder string) bool {
		segments := strings.Split(parentFolder, "/")
		switch segments[0] {
		case "pages":
			fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, parentFolder))
			if err != nil {
				return false
			}
			if fileInfo.IsDir() {
				return true
			}
		case "public":
			if len(segments) < 2 || segments[1] != "themes" {
				return false
			}
			fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, parentFolder))
			if err != nil {
				return false
			}
			if fileInfo.IsDir() {
				return true
			}
		}
		return false
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
				"base":       path.Base,
				"referer":    func() string { return r.Referer() },
				"username":   func() string { return username },
				"sitePrefix": func() string { return sitePrefix },
			}
			tmpl, err := template.New("createfolder.html").Funcs(funcMap).ParseFS(rootFS, "embed/createfolder.html")
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
			if !response.Status.Success() {
				var status string
				switch response.Status {
				case ErrParentFolderNotProvided, ErrInvalidParentFolder:
					status = response.Status.Code() + " Couldn't create item, " + response.Status.Message()
				case ErrForbiddenName:
					status = fmt.Sprintf("%s: %q", response.Status, response.Name)
				case ErrItemAlreadyExists:
					status = fmt.Sprintf("%s folder %q already exists", response.Status.Code(), response.Name)
				default:
					status = string(response.Status)
				}
				err := nbrew.setSession(w, r, "flash", map[string]any{
					"status": status,
				})
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				if response.Status == ErrParentFolderNotProvided || response.Status == ErrInvalidParentFolder {
					http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix)+"/", http.StatusFound)
					return
				}
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix, response.ParentFolder)+"/", http.StatusFound)
				return
			}
			if response.Status == CreateFolderSuccess {
				err := nbrew.setSession(w, r, "flash", map[string]any{
					"status": fmt.Sprintf(
						`%s Created folder <a href="%s" class="linktext">%s</a>`,
						response.Status.Code(),
						"/"+path.Join("admin", sitePrefix, response.ParentFolder, response.Name)+"/",
						response.Name,
					),
				})
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
					internalServerError(w, r, err)
					return
				}
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
			response.Status = ErrForbiddenName
			writeResponse(w, r, response)
			return
		}
		head, tail, _ := strings.Cut(response.ParentFolder, "/")
		if head == "pages" {
			err := nbrew.FS.Mkdir(path.Join(sitePrefix, "public", tail, response.Name), 0755)
			if err != nil && !errors.Is(err, fs.ErrExist) {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		}
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
		err = nbrew.FS.Mkdir(path.Join(sitePrefix, response.ParentFolder, response.Name), 0755)
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
		response.Status = CreateFolderSuccess
		writeResponse(w, r, response)
	default:
		methodNotAllowed(w, r)
	}
}
