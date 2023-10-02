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

	"github.com/bokwoon95/sq"
)

func (nbrew *Notebrew) createsite(w http.ResponseWriter, r *http.Request, username string) {
	type Request struct {
		SiteName string `json:"siteName,omitempty"`
	}
	type Response struct {
		Status           Error              `json:"status"`
		SiteName         string             `json:"siteName,omitempty"`
		CurrentSites     []string           `json:"currentSites,omitempty"` // TODO: not needed, remove.
		ValidationErrors map[string][]Error `json:"validationErrors,omitempty"`
	}

	getCurrentSites := func() ([]string, error) {
		return sq.FetchAllContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "SELECT {*}" +
				" FROM site" +
				" JOIN site_user ON site_user.site_id = site.site_id" +
				" JOIN users ON users.user_id = site_user.user_id" +
				" WHERE users.username = {username}" +
				" AND NOT EXISTS (" +
				"SELECT 1 FROM users WHERE username = site.site_name" +
				")",
			Values: []any{
				sq.StringParam("username", username),
			},
		}, func(row *sq.Row) string {
			return row.String("site.site_name")
		})
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
				"referer":  func() string { return r.Referer() },
				"username": func() string { return username },
			}
			tmpl, err := template.New("createsite.html").Funcs(funcMap).ParseFS(rootFS, "embed/createsite.html")
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
		response.SiteName = r.Form.Get("name")
		response.CurrentSites, err = getCurrentSites()
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if len(response.CurrentSites) >= 10 {
			response.Status = ErrMaxSitesReached
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
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/createsite/", http.StatusFound)
				return
			}
			err := nbrew.setSession(w, r, "flash", map[string]any{
				"status": Error(fmt.Sprintf(`%s Site created`, response.Status.Code())),
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			var sitePrefix string
			if strings.Contains(response.SiteName, ".") {
				sitePrefix = response.SiteName
			} else if response.SiteName != "" {
				sitePrefix = "@" + response.SiteName
			}
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/"+path.Join("admin", sitePrefix)+"/", http.StatusFound)
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
			request.SiteName = r.Form.Get("siteName")
		default:
			unsupportedContentType(w, r)
			return
		}

		var err error
		response := Response{
			SiteName:         request.SiteName,
			ValidationErrors: make(map[string][]Error),
		}

		response.CurrentSites, err = getCurrentSites()
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if len(response.CurrentSites) >= 10 {
			response.Status = ErrMaxSitesReached
			writeResponse(w, r, response)
			return
		}

		if response.SiteName == "" {
			response.ValidationErrors["siteName"] = append(response.ValidationErrors["siteName"], ErrRequired)
		} else {
			hasForbiddenCharacters := false
			digitCount := 0
			for _, char := range response.SiteName {
				if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '.' {
					hasForbiddenCharacters = true
				}
				if char >= '0' && char <= '9' {
					digitCount++
				}
			}
			if hasForbiddenCharacters {
				response.ValidationErrors["siteName"] = append(response.ValidationErrors["siteName"], Error(string(ErrForbiddenCharacters)+" - only lowercase letters, numbers and hyphen allowed"))
			}
			if len(response.SiteName) > 30 {
				response.ValidationErrors["siteName"] = append(response.ValidationErrors["siteName"], Error(string(ErrTooLong)+" - cannot exceed 30 characters"))
			}
		}
		if len(response.ValidationErrors["siteName"]) == 0 {
			exists, err := sq.FetchExistsContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format:  "SELECT 1 FROM site WHERE site_name = {siteName}",
				Values: []any{
					sq.StringParam("siteName", response.SiteName),
				},
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if exists {
				response.ValidationErrors["siteName"] = append(response.ValidationErrors["siteName"], ErrUnavailable)
			}
		}
		if len(response.ValidationErrors) > 0 {
			response.Status = ErrValidationFailed
			writeResponse(w, r, response)
			return
		}

		var sitePrefix string
		if strings.Contains(response.SiteName, ".") {
			sitePrefix = response.SiteName
		} else if response.SiteName != "" {
			sitePrefix = "@" + response.SiteName
		}
		err = nbrew.FS.Mkdir(sitePrefix, 0755)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		dirs := []string{
			"notes",
			"pages",
			"posts",
			"public",
			"public/images",
			"public/themes",
			"system",
		}
		for _, dir := range dirs {
			err = nbrew.FS.Mkdir(path.Join(sitePrefix, dir), 0755)
			if err != nil && !errors.Is(err, fs.ErrExist) {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		}
		if nbrew.DB != nil {
			tx, err := nbrew.DB.Begin()
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			defer tx.Rollback()
			siteID := NewID()
			_, err = sq.ExecContext(r.Context(), tx, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "INSERT INTO site (site_id, site_name)" +
					" VALUES ({siteID}, {siteName})",
				Values: []any{
					sq.UUIDParam("siteID", siteID),
					sq.StringParam("siteName", request.SiteName),
				},
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			_, err = sq.ExecContext(r.Context(), tx, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "INSERT INTO site_user (site_id, user_id)" +
					" VALUES ((SELECT site_id FROM site WHERE site_name = {siteName}), (SELECT user_id FROM users WHERE username = {username}))",
				Values: []any{
					sq.StringParam("siteName", request.SiteName),
					sq.StringParam("username", username),
				},
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			err = tx.Commit()
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
		}
		response.Status = CreateSiteSuccess
		writeResponse(w, r, response)
	default:
		methodNotAllowed(w, r)
	}
}
