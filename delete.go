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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (nbrew *Notebrew) delet(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Item struct {
		Name       string    `json:"name,omitempty"`
		IsDir      bool      `json:"isDir,omitempty"`
		Size       int64     `json:"size,omitempty"`
		ModTime    time.Time `json:"modTime,omitempty"`
		NumFolders int       `json:"numFolders,omitEmpty"`
		NumFiles   int       `json:"numFiles,omitEmpty"`
	}
	type Request struct {
		ParentFolder string   `json:"parentFolder,omitempty"`
		Names        []string `json:"names,omitempty"`
	}
	type Response struct {
		Status       Error    `json:"status"`
		ParentFolder string   `json:"parentFolder,omitempty"`
		Items        []Item   `json:"items,omitempty"`
		Errors       []string `json:"errors,omitempty"`
	}

	isValidParentFolder := func(parentFolder string) bool {
		segments := strings.Split(parentFolder, "/")
		switch segments[0] {
		case "notes", "pages", "posts":
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
				"referer":    func() string { return r.Referer() },
				"username":   func() string { return username },
				"sitePrefix": func() string { return sitePrefix },
				"filecount": func(numFolders, numFiles int) string {
					if numFolders == 0 && numFiles == 0 {
						return "no files"
					}
					parts := make([]string, 0, 2)
					if numFolders == 1 {
						parts = append(parts, "1 folder")
					} else if numFolders > 1 {
						parts = append(parts, strconv.Itoa(numFolders)+" folders")
					}
					if numFiles == 1 {
						parts = append(parts, "1 file")
					} else if numFiles > 1 {
						parts = append(parts, strconv.Itoa(numFiles)+" files")
					}
					return strings.Join(parts, ", ")
				},
			}
			tmpl, err := template.New("delete.html").Funcs(funcMap).ParseFS(rootFS, "embed/delete.html")
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
		seen := make(map[string]bool)
		for _, name := range r.Form["name"] {
			name = filepath.ToSlash(name)
			if strings.Contains(name, "/") {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, name))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			var numFolders, numFiles int
			if fileInfo.IsDir() {
				dirEntries, err := fs.ReadDir(nbrew.FS, path.Join(sitePrefix, response.ParentFolder, name))
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				for _, dirEntry := range dirEntries {
					if dirEntry.IsDir() {
						numFolders++
					} else {
						numFiles++
					}
				}
			}
			response.Items = append(response.Items, Item{
				Name:       fileInfo.Name(),
				IsDir:      fileInfo.IsDir(),
				Size:       fileInfo.Size(),
				ModTime:    fileInfo.ModTime(),
				NumFolders: numFolders,
				NumFiles:   numFiles,
			})
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
			var status, redirectURL string
			if response.Status.Equal(ErrParentFolderNotProvided) || response.Status.Equal(ErrInvalidParentFolder) {
				status = response.Status.Code() + " Couldn't delete item(s), " + response.Status.Message()
				redirectURL = nbrew.Scheme + nbrew.AdminDomain + "/" + path.Join("admin", sitePrefix) + "/"
			} else {
				status = string(response.Status)
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
			request.Names = r.Form["name"]
		default:
			unsupportedContentType(w, r)
			return
		}

		var response Response
		if request.ParentFolder == "" {
			response.Status = ErrParentFolderNotProvided
			writeResponse(w, r, response)
		}
		response.ParentFolder = path.Clean(strings.Trim(request.ParentFolder, "/"))
		if !isValidParentFolder(response.ParentFolder) {
			response.Status = ErrInvalidParentFolder
			writeResponse(w, r, response)
			return
		}
		seen := make(map[string]bool)
		for _, name := range request.Names {
			name = filepath.ToSlash(name)
			if strings.Contains(name, "/") {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			err := RemoveAll(nbrew.FS, path.Join(sitePrefix, request.ParentFolder, name))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				response.Errors = append(response.Errors, fmt.Sprintf("%s: %v", name, err))
			} else {
				response.Items = append(response.Items, Item{Name: name})
			}
		}
		var b strings.Builder
		if len(response.Errors) == 0 {
			b.WriteString(DeleteSuccess.Code() + " ")
		} else {
			b.WriteString(ErrDeleteFailed.Code() + " ")
		}
		if len(response.Items) == 1 {
			b.WriteString("1 item deleted")
		} else {
			b.WriteString(strconv.Itoa(len(response.Items)) + " items deleted")
		}
		if len(response.Errors) == 1 {
			b.WriteString(" (1 item failed)")
		} else if len(response.Errors) > 1 {
			b.WriteString(" (" + strconv.Itoa(len(response.Errors)) + " items failed)")
		}
		response.Status = Error(b.String())
		writeResponse(w, r, response)
	default:
		methodNotAllowed(w, r)
	}
}
