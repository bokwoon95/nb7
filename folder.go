package nb7

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bokwoon95/sq"
)

func (nbrew *Notebrew) folder(w http.ResponseWriter, r *http.Request, username, sitePrefix, folderPath string, fileInfo fs.FileInfo) {
	type Entry struct {
		Name       string     `json:"name,omitempty"`
		IsDir      bool       `json:"isDir,omitempty"`
		IsSite     bool       `json:"isSite,omitempty"`
		IsUser     bool       `json:"isUser,omitempty"`
		Title      string     `json:"title,omitempty"`
		Preview    string     `json:"preview,omitempty"`
		Size       int64      `json:"size,omitempty"`
		ModTime    *time.Time `json:"modTime,omitempty"`
		NumFolders int        `json:"numFolders,omitempty"`
		NumFiles   int        `json:"numFiles,omitempty"`
	}
	type Response struct {
		Status         Error      `json:"status"`
		ContentSiteURL string     `json:"contentSiteURL,omitempty"`
		Path           string     `json:"path"`
		IsDir          bool       `json:"isDir,omitempty"`
		ModTime        *time.Time `json:"modTime,omitempty"`
		Entries        []Entry    `json:"entries,omitempty"`
		Sort           string     `json:"sort,omitempty"`
		Order          string     `json:"order,omitempty"`
	}
	if r.Method != "GET" {
		methodNotAllowed(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20 /* 2MB */)
	err := r.ParseForm()
	if err != nil {
		badRequest(w, r, err)
		return
	}
	if fileInfo == nil {
		fileInfo, err = fs.Stat(nbrew.FS, path.Join(sitePrefix, folderPath))
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
	}
	var response Response
	_, err = nbrew.getSession(r, "flash", &response)
	if err != nil {
		getLogger(r.Context()).Error(err.Error())
	}
	nbrew.clearSession(w, r, "flash")
	response.Path = folderPath
	response.IsDir = fileInfo.IsDir()
	if response.Status == "" {
		response.Status = Success
	}

	head, _, _ := strings.Cut(folderPath, "/")
	response.Sort = strings.ToLower(strings.TrimSpace(r.Form.Get("sort")))
	if response.Sort == "" {
		cookie, _ := r.Cookie("sort")
		if cookie != nil {
			response.Sort = cookie.Value
		}
	}
	switch response.Sort {
	case "name", "created", "edited", "title":
		break
	default:
		if head == "notes" || head == "posts" {
			response.Sort = "created"
		} else {
			response.Sort = "name"
		}
	}

	response.Order = strings.ToLower(strings.TrimSpace(r.Form.Get("order")))
	if response.Order == "" {
		cookie, _ := r.Cookie("order")
		if cookie != nil {
			response.Order = cookie.Value
		}
	}
	switch response.Order {
	case "asc", "desc":
		break
	default:
		if response.Sort == "created" || response.Sort == "edited" {
			response.Order = "desc"
		} else {
			response.Order = "asc"
		}
	}

	var folderEntries []Entry
	var fileEntries []Entry

	var notAuthorizedForRootSite bool
	if folderPath == "" {
		// If folderPath empty, show notes/, pages/, posts/, output/ folders.
		if nbrew.DB != nil && sitePrefix == "" {
			// If database is present and sitePrefix is empty, show site
			// folders the user is authorized for.
			type RootFolder struct {
				Name   string
				IsUser bool
			}
			rootFolders, err := sq.FetchAllContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "SELECT {*}" +
					" FROM site" +
					" JOIN site_user ON site_user.site_id = site.site_id" +
					" JOIN users ON users.user_id = site_user.user_id" +
					" WHERE users.username = {username}" +
					" ORDER BY site_prefix",
				Values: []any{
					sq.StringParam("username", username),
				},
			}, func(row *sq.Row) RootFolder {
				return RootFolder{
					Name: row.String("CASE" +
						" WHEN site.site_name LIKE '%.%' THEN site.site_name" +
						" WHEN site.site_name <> '' THEN '@' || site.site_name" +
						" ELSE ''" +
						" END AS site_prefix",
					),
					IsUser: row.Bool("EXISTS (SELECT 1 FROM users WHERE username = site.site_name)"),
				}
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			hasRootSite := slices.ContainsFunc(rootFolders, func(rootFolder RootFolder) bool {
				return rootFolder.Name == ""
			})
			if hasRootSite {
				rootFolders = append(rootFolders,
					RootFolder{Name: "notes"},
					RootFolder{Name: "output"},
					RootFolder{Name: "pages"},
					RootFolder{Name: "posts"},
					RootFolder{Name: "output/themes"},
				)
			} else {
				notAuthorizedForRootSite = true
			}
			for _, rootFolder := range rootFolders {
				if rootFolder.Name == "" {
					continue
				}
				fileInfo, err := fs.Stat(nbrew.FS, rootFolder.Name)
				if err != nil {
					if !errors.Is(err, fs.ErrNotExist) {
						getLogger(r.Context()).Error(err.Error())
					}
					continue
				}
				if !fileInfo.IsDir() {
					continue
				}
				folderEntry := Entry{
					Name:   rootFolder.Name,
					IsDir:  true,
					IsSite: strings.HasPrefix(rootFolder.Name, "@") || strings.Contains(rootFolder.Name, "."),
					IsUser: rootFolder.IsUser,
				}
				modTime := fileInfo.ModTime()
				if !modTime.IsZero() {
					folderEntry.ModTime = &modTime
				}
				folderEntries = append(folderEntries, folderEntry)
			}
		} else {
			// path.Clean the sitePrefix in case it is an empty string, in
			// which case it becomes "."
			dirEntries, err := nbrew.FS.ReadDir(path.Clean(sitePrefix))
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			for _, dirEntry := range dirEntries {
				entry := Entry{
					Name:  dirEntry.Name(),
					IsDir: dirEntry.IsDir(),
				}
				if !entry.IsDir {
					continue
				}
				switch entry.Name {
				case "notes", "pages", "posts":
					folderEntries = append(folderEntries, entry)
				case "output":
					folderEntries = append(folderEntries, entry)
					fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, "output/themes"))
					if err != nil && !errors.Is(err, fs.ErrNotExist) {
						getLogger(r.Context()).Error(err.Error())
						internalServerError(w, r, err)
						return
					}
					if fileInfo != nil && fileInfo.IsDir() {
						folderEntries = append(folderEntries, Entry{
							Name:  "output/themes",
							IsDir: true,
						})
					}
				default:
					if sitePrefix != "" {
						continue
					}
					if !strings.HasPrefix(entry.Name, "@") && !strings.Contains(entry.Name, ".") {
						continue
					}
					entry.IsSite = true
					folderEntries = append(folderEntries, entry)
				}
			}
		}
	} else {
		dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, folderPath))
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		for _, dirEntry := range dirEntries {
			fileInfo, err := dirEntry.Info()
			if err != nil {
				getLogger(r.Context()).Error(err.Error(), slog.String("name", dirEntry.Name()))
				internalServerError(w, r, err)
				return
			}
			entry := Entry{
				Name:  dirEntry.Name(),
				IsDir: dirEntry.IsDir(),
				Size:  fileInfo.Size(),
			}
			modTime := fileInfo.ModTime()
			if !modTime.IsZero() {
				entry.ModTime = &modTime
			}
			ext := path.Ext(entry.Name)
			switch head {
			case "notes", "posts":
				if entry.IsDir {
					folderEntries = append(folderEntries, entry)
					continue
				}
				if ext != ".md" && ext != ".txt" {
					fileEntries = append(fileEntries, entry)
					continue
				}
				file, err := nbrew.FS.Open(path.Join(sitePrefix, response.Path, entry.Name))
				if err != nil {
					getLogger(r.Context()).Error(err.Error(), slog.String("name", entry.Name))
					internalServerError(w, r, err)
					return
				}
				reader := bufio.NewReader(file)
				done := false
				for {
					if done {
						break
					}
					line, err := reader.ReadBytes('\n')
					if err != nil {
						done = true
					}
					line = bytes.TrimSpace(line)
					if len(line) == 0 {
						continue
					}
					if entry.Title == "" {
						var b strings.Builder
						stripMarkdownStyles(&b, line)
						entry.Title = b.String()
						continue
					}
					if entry.Preview == "" {
						var b strings.Builder
						stripMarkdownStyles(&b, line)
						entry.Preview = b.String()
						continue
					}
					break
				}
				fileEntries = append(fileEntries, entry)
				err = file.Close()
				if err != nil {
					getLogger(r.Context()).Error(err.Error(), slog.String("name", entry.Name))
					internalServerError(w, r, err)
					return
				}
			default:
				if entry.IsDir {
					folderEntries = append(folderEntries, entry)
					continue
				}
				fileEntries = append(fileEntries, entry)
			}
		}
	}

	for i, folderEntry := range folderEntries {
		if strings.HasPrefix(folderEntry.Name, "@") || strings.Contains(folderEntry.Name, ".") {
			continue
		}
		dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, folderPath, folderEntry.Name))
		if err != nil {
			getLogger(r.Context()).Error(err.Error(), slog.String("name", folderEntry.Name))
			internalServerError(w, r, err)
			return
		}
		for _, dirEntry := range dirEntries {
			if dirEntry.IsDir() {
				folderEntries[i].NumFolders++
			} else {
				folderEntries[i].NumFiles++
			}
		}
	}
	slices.SortFunc(folderEntries, func(a, b Entry) int {
		isSite1 := strings.HasPrefix(a.Name, "@") || strings.Contains(a.Name, ".")
		isSite2 := strings.HasPrefix(b.Name, "@") || strings.Contains(b.Name, ".")
		if isSite1 && !isSite2 {
			return 1
		}
		if !isSite1 && isSite2 {
			return -1
		}
		return strings.Compare(path.Base(a.Name), path.Base(b.Name))
	})

	switch response.Sort {
	case "name", "created":
		if response.Order == "desc" {
			slices.Reverse(fileEntries)
		}
	case "edited":
		slices.SortFunc(fileEntries, func(a, b Entry) int {
			var cmp int
			if a.ModTime == nil && b.ModTime == nil {
				cmp = 0
			} else if a.ModTime == nil {
				cmp = -1
			} else if b.ModTime == nil {
				cmp = 1
			} else {
				if a.ModTime.Equal(*b.ModTime) {
					cmp = 0
				} else if a.ModTime.Before(*b.ModTime) {
					cmp = -1
				} else {
					cmp = 1
				}
			}
			if response.Order == "asc" {
				return cmp
			}
			return -cmp
		})
	case "title":
		if head == "notes" || head == "posts" {
			slices.SortFunc(fileEntries, func(a, b Entry) int {
				var cmp int
				if a.Title == b.Title {
					cmp = 0
				} else if a.Title < b.Title {
					cmp = -1
				} else {
					cmp = 1
				}
				if response.Order == "asc" {
					return cmp
				}
				return -cmp
			})
		}
	}

	response.Entries = make([]Entry, 0, len(folderEntries)+len(fileEntries))
	response.Entries = append(response.Entries, folderEntries...)
	response.Entries = append(response.Entries, fileEntries...)

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
		"join":                  path.Join,
		"base":                  path.Base,
		"ext":                   path.Ext,
		"trimPrefix":            strings.TrimPrefix,
		"neatenURL":             neatenURL,
		"fileSizeToString":      fileSizeToString,
		"isEven":                func(i int) bool { return i%2 == 0 },
		"username":              func() string { return username },
		"referer":               func() string { return r.Referer() },
		"sitePrefix":            func() string { return sitePrefix },
		"safeHTML":              func(s string) template.HTML { return template.HTML(s) },
		"authorizedForRootSite": func() bool { return !notAuthorizedForRootSite },
		"head": func(s string) string {
			head, _, _ := strings.Cut(s, "/")
			return head
		},
		"tail": func(s string) string {
			_, tail, _ := strings.Cut(s, "/")
			return tail
		},
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
		"generateBreadcrumbLinks": func(filePath string) template.HTML {
			var b strings.Builder
			b.WriteString(`<a href="/admin/" class="linktext ma1">admin</a>`)
			segments := strings.Split(filePath, "/")
			if sitePrefix != "" {
				segments = append([]string{sitePrefix}, segments...)
			}
			for i := 0; i < len(segments); i++ {
				if segments[i] == "" {
					continue
				}
				href := `/admin/` + path.Join(segments[:i+1]...) + `/`
				if i == len(segments)-1 && !response.IsDir {
					href = strings.TrimSuffix(href, `/`)
				}
				b.WriteString(`/<a href="` + href + `" class="linktext ma1">` + segments[i] + `</a>`)
			}
			if response.IsDir {
				b.WriteString(`/`)
			}
			return template.HTML(b.String())
		},
	}
	tmpl, err := template.New("folder.html").Funcs(funcMap).ParseFS(rootFS, "embed/folder.html")
	if err != nil {
		getLogger(r.Context()).Error(err.Error())
		internalServerError(w, r, err)
		return
	}
	executeTemplate(w, r, fileInfo.ModTime(), tmpl, &response)
}
