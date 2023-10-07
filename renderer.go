package nb7

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"text/template/parse"
	"time"

	"golang.org/x/crypto/blake2b"
)

type Renderer struct {
	nbrew      *Notebrew
	sitePrefix string
	mu         *sync.RWMutex // protects cache and errmsgs
	cache      map[string]*template.Template
	errmsgs    map[string][]string
	funcMap    map[string]any
	ctx        context.Context
}

func NewRenderer(ctx context.Context, nbrew *Notebrew, sitePrefix string) *Renderer {
	renderer := &Renderer{
		nbrew:      nbrew,
		sitePrefix: sitePrefix,
		mu:         &sync.RWMutex{},
		cache:      make(map[string]*template.Template),
		errmsgs:    make(url.Values),
		ctx:        ctx,
	}
	siteName := strings.TrimPrefix(sitePrefix, "@")
	adminURL := nbrew.Scheme + nbrew.AdminDomain
	siteURL := nbrew.Scheme + nbrew.ContentDomain
	if strings.Contains(siteName, ".") {
		siteURL = "https://" + siteName
	} else if siteName != "" {
		switch nbrew.MultisiteMode {
		case "subdomain":
			siteURL = nbrew.Scheme + siteName + "." + nbrew.ContentDomain
		case "subdirectory":
			siteURL = nbrew.Scheme + nbrew.ContentDomain + "/" + sitePrefix
		}
	}
	var shortSiteURL string
	if strings.HasPrefix(siteURL, "https://") {
		shortSiteURL = strings.TrimSuffix(strings.TrimPrefix(siteURL, "https://"), "/")
	} else {
		shortSiteURL = strings.TrimSuffix(strings.TrimPrefix(siteURL, "http://"), "/")
	}
	renderer.funcMap = map[string]any{
		"join":             path.Join,
		"base":             path.Base,
		"ext":              path.Ext,
		"trimPrefix":       strings.TrimPrefix,
		"trimSuffix":       strings.TrimSuffix,
		"fileSizeToString": fileSizeToString,
		"adminURL":         func() string { return adminURL },
		"siteURL":          func() string { return siteURL },
		"shortSiteURL":     func() string { return shortSiteURL },
		"safeHTML":         func(s string) template.HTML { return template.HTML(s) },
		"head": func(s string) string {
			head, _, _ := strings.Cut(s, "/")
			return head
		},
		"tail": func(s string) string {
			_, tail, _ := strings.Cut(s, "/")
			return tail
		},
		"list": func(v ...any) []any { return v },
		"dict": func(v ...any) (map[string]any, error) {
			dict := make(map[string]any)
			if len(dict)%2 != 0 {
				return nil, fmt.Errorf("odd number of arguments passed in")
			}
			for i := 0; i+1 < len(dict); i += 2 {
				key, ok := v[i].(string)
				if !ok {
					return nil, fmt.Errorf("value %d (%#v) is not a string", i, v[i])
				}
				value := v[i+1]
				dict[key] = value
			}
			return dict, nil
		},
		"getPosts": func(category string) ([]Post, error) {
			return nbrew.getPosts(ctx, sitePrefix, category)
		},
	}
	return renderer
}

func (renderer *Renderer) Render(w io.Writer, text string) error {
	tmpl, err := renderer.parse("", text, nil)
	if err != nil {
		return err
	}
	err = tmpl.Execute(w, nil)
	if err != nil {
		return err
	}
	return nil
}

func (renderer *Renderer) RenderPost(w io.Writer, content []byte, creationDate, lastModified time.Time) error {
	file, err := renderer.nbrew.FS.Open(path.Join(renderer.sitePrefix, "output/themes/post.html"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		file, err = rootFS.Open("static/post.html")
		if err != nil {
			return err
		}
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.Grow(int(fileInfo.Size()))
	_, err = io.Copy(&b, file)
	if err != nil {
		return err
	}
	tmpl, err := renderer.parse("", b.String(), nil)
	if err != nil {
		return err
	}
	post := Post{
		CreationDate: creationDate,
		LastModified: lastModified,
	}
	var line []byte
	remainder := content
	for len(remainder) > 0 {
		line, remainder, _ = bytes.Cut(remainder, []byte("\n"))
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var b strings.Builder
		stripMarkdownStyles(&b, line)
		post.Title = b.String()
		break
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	err = goldmarkMarkdown.Convert(content, buf)
	if err != nil {
		return err
	}
	post.Content = template.HTML(buf.String())
	err = tmpl.Execute(w, &post)
	if err != nil {
		renderer.mu.Lock()
		renderer.errmsgs[""] = append(renderer.errmsgs[""], err.Error())
		renderer.mu.Unlock()
		return RenderError(renderer.errmsgs)
	}
	return nil
}

func (renderer *Renderer) RenderPostIndex(w io.Writer) error {
	file, err := renderer.nbrew.FS.Open(path.Join(renderer.sitePrefix, "output/themes/posts.html"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		file, err = rootFS.Open("static/posts.html")
		if err != nil {
			return err
		}
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.Grow(int(fileInfo.Size()))
	_, err = io.Copy(&b, file)
	if err != nil {
		return err
	}
	tmpl, err := renderer.parse("", b.String(), nil)
	if err != nil {
		return err
	}
	err = tmpl.Execute(w, nil)
	if err != nil {
		renderer.mu.Lock()
		renderer.errmsgs[""] = append(renderer.errmsgs[""], err.Error())
		renderer.mu.Unlock()
		return RenderError(renderer.errmsgs)
	}
	return nil
}

func (renderer *Renderer) parse(templateName, templateText string, callers []string) (*template.Template, error) {
	primaryTemplate, err := template.New(templateName).Funcs(renderer.funcMap).Parse(templateText)
	if err != nil {
		errmsg := err.Error()
		// TODO: collect all possible error strings then use string
		// manipulation to format the errmsg into something the user can
		// understand. E.g. if the template name is an empty string, how to
		// make the error more obvious?
		renderer.mu.Lock()
		renderer.errmsgs[templateName] = append(renderer.errmsgs[templateName], errmsg)
		renderer.mu.Unlock()
		return nil, RenderError(renderer.errmsgs)
	}
	primaryTemplates := primaryTemplate.Templates()
	slices.SortFunc(primaryTemplates, func(t1, t2 *template.Template) int {
		return strings.Compare(t1.Name(), t2.Name())
	})
	for _, tmpl := range primaryTemplates {
		name := tmpl.Name()
		if name != templateName && strings.HasSuffix(name, ".html") {
			renderer.mu.Lock()
			renderer.errmsgs[templateName] = append(renderer.errmsgs[templateName], fmt.Sprintf("%s: define %q: defined template's name cannot end in .html", templateName, name))
			renderer.mu.Unlock()
		}
	}
	renderer.mu.RLock()
	errmsgs := renderer.errmsgs
	renderer.mu.RUnlock()
	if len(errmsgs) > 0 {
		return nil, RenderError(errmsgs)
	}
	var names []string
	var node parse.Node
	var nodes []parse.Node
	for _, tmpl := range primaryTemplates {
		if tmpl.Tree == nil {
			continue
		}
		nodes = append(nodes, tmpl.Tree.Root.Nodes...)
		for len(nodes) > 0 {
			node, nodes = nodes[len(nodes)-1], nodes[:len(nodes)-1]
			switch node := node.(type) {
			case *parse.ListNode:
				if node == nil {
					continue
				}
				nodes = append(nodes, node.Nodes...)
			case *parse.BranchNode:
				nodes = append(nodes, node.List, node.ElseList)
			case *parse.RangeNode:
				nodes = append(nodes, node.List, node.ElseList)
			case *parse.TemplateNode:
				if strings.HasSuffix(node.Name, ".html") {
					names = append(names, node.Name)
				}
			}
		}
	}
	finalTemplate := template.New(templateName).Funcs(renderer.funcMap)
	slices.SortFunc(names, func(name1, name2 string) int {
		return -strings.Compare(name1, name2)
	})
	names = slices.Compact(names)
	for _, name := range names {
		err := renderer.ctx.Err()
		if err != nil {
			return nil, err
		}
		renderer.mu.RLock()
		tmpl := renderer.cache[name]
		renderer.mu.RUnlock()
		if tmpl == nil {
			file, err := renderer.nbrew.FS.Open(path.Join(renderer.sitePrefix, "output/themes", name))
			if errors.Is(err, fs.ErrNotExist) {
				renderer.mu.Lock()
				renderer.errmsgs[name] = append(renderer.errmsgs[name], fmt.Sprintf("%s calls nonexistent template %q", templateName, name))
				renderer.mu.Unlock()
				continue
			}
			if slices.Contains(callers, name) {
				renderer.mu.Lock()
				renderer.errmsgs[callers[0]] = append(renderer.errmsgs[callers[0]], fmt.Sprintf(
					"calling %s ends in a circular reference: %s",
					callers[0],
					strings.Join(append(callers, name), " => "),
				))
				renderer.mu.Unlock()
				return nil, RenderError(renderer.errmsgs)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: open %s: %w", templateName, name, err)
			}
			fileinfo, err := file.Stat()
			if err != nil {
				return nil, fmt.Errorf("%s: stat %s: %w", templateName, name, err)
			}
			var b strings.Builder
			b.Grow(int(fileinfo.Size()))
			_, err = io.Copy(&b, file)
			if err != nil {
				return nil, fmt.Errorf("%s: read %s: %w", templateName, name, err)
			}
			err = file.Close()
			if err != nil {
				return nil, fmt.Errorf("%s: close %s: %w", templateName, name, err)
			}
			text := b.String()
			tmpl, err = renderer.parse(name, text, append(callers, name))
			if err != nil {
				return nil, err
			}
			if tmpl == nil {
				continue
			}
			renderer.mu.Lock()
			renderer.cache[name] = tmpl
			renderer.mu.Unlock()
		}
		for _, tmpl := range tmpl.Templates() {
			_, err = finalTemplate.AddParseTree(tmpl.Name(), tmpl.Tree)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: add %s: %w", templateName, name, tmpl.Name(), err)
			}
		}
	}
	renderer.mu.RLock()
	errmsgs = renderer.errmsgs
	renderer.mu.RUnlock()
	if len(errmsgs) > 0 {
		return nil, RenderError(errmsgs)
	}
	for _, tmpl := range primaryTemplates {
		_, err = finalTemplate.AddParseTree(tmpl.Name(), tmpl.Tree)
		if err != nil {
			return nil, fmt.Errorf("%s: add %s: %w", templateName, tmpl.Name(), err)
		}
	}
	return finalTemplate, nil
}

type RenderError map[string][]string

func (renderError RenderError) Error() string {
	names := make([]string, 0, len(renderError))
	for name := range renderError {
		names = append(names, name)
	}
	return fmt.Sprintf("the following templates have errors: %+v", names)
}

func (renderError RenderError) ToList() []string {
	var list []string
	names := make([]string, 0, len(renderError))
	for name := range renderError {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, errmsg := range renderError[name] {
			list = append(list, errmsg)
		}
	}
	return list
}

var gzipPool = sync.Pool{
	New: func() any {
		// Use compression level 4 for best balance between space and
		// performance.
		// https://blog.klauspost.com/gzip-performance-for-go-webservers/
		gzipWriter, _ := gzip.NewWriterLevel(nil, 4)
		return gzipWriter
	},
}

var hashPool = sync.Pool{
	New: func() any {
		hash, err := blake2b.New256(nil)
		if err != nil {
			panic(err)
		}
		return hash
	},
}

var bytesPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 64)
		return &b
	},
}

func executeTemplate(w http.ResponseWriter, r *http.Request, modtime time.Time, tmpl *template.Template, data any) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	hasher := hashPool.Get().(hash.Hash)
	hasher.Reset()
	defer hashPool.Put(hasher)

	multiWriter := io.MultiWriter(buf, hasher)
	gzipWriter := gzipPool.Get().(*gzip.Writer)
	gzipWriter.Reset(multiWriter)
	defer gzipPool.Put(gzipWriter)

	err := tmpl.Execute(gzipWriter, data)
	if err != nil {
		getLogger(r.Context()).Error(err.Error(), slog.String("data", fmt.Sprintf("%#v", data)))
		internalServerError(w, r, err)
		return
	}
	err = gzipWriter.Close()
	if err != nil {
		getLogger(r.Context()).Error(err.Error())
		internalServerError(w, r, err)
		return
	}

	src := bytesPool.Get().(*[]byte)
	*src = (*src)[:0]
	defer bytesPool.Put(src)

	dst := bytesPool.Get().(*[]byte)
	*dst = (*dst)[:0]
	defer bytesPool.Put(dst)

	*src = hasher.Sum(*src)
	encodedLen := hex.EncodedLen(len(*src))
	if cap(*dst) < encodedLen {
		*dst = make([]byte, encodedLen)
	}
	*dst = (*dst)[:encodedLen]
	hex.Encode(*dst, *src)

	if _, ok := w.Header()["Content-Security-Policy"]; !ok {
		w.Header().Set("Content-Security-Policy", defaultContentSecurityPolicy)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", string(*dst))
	http.ServeContent(w, r, "", modtime, bytes.NewReader(buf.Bytes()))
}
