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

type TemplateParser struct {
	nbrew      *Notebrew
	sitePrefix string
	mu         *sync.RWMutex // protects cache and errmsgs
	cache      map[string]*template.Template
	errmsgs    map[string][]string
	funcMap    map[string]any
}

func NewTemplateParser(nbrew *Notebrew, sitePrefix string) *TemplateParser {
	parser := &TemplateParser{
		nbrew:      nbrew,
		sitePrefix: sitePrefix,
		mu:         &sync.RWMutex{},
		cache:      make(map[string]*template.Template),
		errmsgs:    make(url.Values),
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
	parser.funcMap = map[string]any{
		"join":             path.Join,
		"base":             path.Base,
		"ext":              path.Ext,
		"trimPrefix":       strings.TrimPrefix,
		"trimSuffix":       strings.TrimSuffix,
		"fileSizeToString": fileSizeToString,
		"adminURL":         func() string { return adminURL },
		"siteURL":          func() string { return siteURL },
		"shortSiteURL":     func() string { return shortSiteURL },
		"isEven":           func(i int) bool { return i%2 == 0 },
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
		// TODO: "getPosts(category string) []Post"
	}
	return parser
}

func (parser *TemplateParser) Parse(ctx context.Context, templateName, templateText string) (*template.Template, error) {
	return parser.parse(ctx, templateName, templateText, nil)
}

func (parser *TemplateParser) parse(ctx context.Context, templateName, templateText string, callers []string) (*template.Template, error) {
	primaryTemplate, err := template.New(templateName).Funcs(parser.funcMap).Parse(templateText)
	if err != nil {
		parser.mu.Lock()
		parser.errmsgs[templateName] = append(parser.errmsgs[templateName], strings.TrimSpace(strings.TrimPrefix(err.Error(), "template:")))
		parser.mu.Unlock()
		return nil, TemplateErrors(parser.errmsgs)
	}
	primaryTemplates := primaryTemplate.Templates()
	slices.SortFunc(primaryTemplates, func(t1, t2 *template.Template) int {
		return strings.Compare(t1.Name(), t2.Name())
	})
	for _, tmpl := range primaryTemplates {
		name := tmpl.Name()
		if name != templateName && strings.HasSuffix(name, ".html") {
			parser.mu.Lock()
			parser.errmsgs[templateName] = append(parser.errmsgs[templateName], fmt.Sprintf("%s: define %q: defined template's name cannot end in .html", templateName, name))
			parser.mu.Unlock()
		}
	}
	parser.mu.RLock()
	errmsgs := parser.errmsgs
	parser.mu.RUnlock()
	if len(errmsgs) > 0 {
		return nil, TemplateErrors(errmsgs)
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
	finalTemplate := template.New(templateName).Funcs(parser.funcMap)
	slices.SortFunc(names, func(name1, name2 string) int {
		return -strings.Compare(name1, name2)
	})
	names = slices.Compact(names)
	for _, name := range names {
		err := ctx.Err()
		if err != nil {
			return nil, err
		}
		parser.mu.RLock()
		tmpl := parser.cache[name]
		parser.mu.RUnlock()
		if tmpl == nil {
			file, err := parser.nbrew.FS.Open(path.Join(parser.sitePrefix, "output/themes", name))
			if errors.Is(err, fs.ErrNotExist) {
				parser.mu.Lock()
				parser.errmsgs[name] = append(parser.errmsgs[name], fmt.Sprintf("%s calls nonexistent template %q", templateName, name))
				parser.mu.Unlock()
				continue
			}
			if slices.Contains(callers, name) {
				parser.mu.Lock()
				parser.errmsgs[callers[0]] = append(parser.errmsgs[callers[0]], fmt.Sprintf(
					"calling %s ends in a circular reference: %s",
					callers[0],
					strings.Join(append(callers, name), " => "),
				))
				parser.mu.Unlock()
				return nil, TemplateErrors(parser.errmsgs)
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
			tmpl, err = parser.parse(ctx, name, text, append(callers, name))
			if err != nil {
				return nil, err
			}
			if tmpl == nil {
				continue
			}
			parser.mu.Lock()
			parser.cache[name] = tmpl
			parser.mu.Unlock()
		}
		for _, tmpl := range tmpl.Templates() {
			_, err = finalTemplate.AddParseTree(tmpl.Name(), tmpl.Tree)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: add %s: %w", templateName, name, tmpl.Name(), err)
			}
		}
	}
	parser.mu.RLock()
	errmsgs = parser.errmsgs
	parser.mu.RUnlock()
	if len(errmsgs) > 0 {
		return nil, TemplateErrors(errmsgs)
	}
	for _, tmpl := range primaryTemplates {
		_, err = finalTemplate.AddParseTree(tmpl.Name(), tmpl.Tree)
		if err != nil {
			return nil, fmt.Errorf("%s: add %s: %w", templateName, tmpl.Name(), err)
		}
	}
	return finalTemplate, nil
}

func (nbrew *Notebrew) parseTemplate(sitePrefix string, cache map[string]*template.Template, errmsgs map[string][]string, callers []string, templateName, templateText string) (*template.Template, error) {
	primaryTemplate, err := template.New(templateName).Funcs(commonFuncMap).Parse(templateText)
	if err != nil {
		errmsgs[templateName] = append(errmsgs[templateName], strings.TrimSpace(strings.TrimPrefix(err.Error(), "template:")))
		return nil, nil
	}
	primaryTemplates := primaryTemplate.Templates()
	slices.SortFunc(primaryTemplates, func(t1, t2 *template.Template) int {
		return strings.Compare(t1.Name(), t2.Name())
	})
	for _, tmpl := range primaryTemplates {
		name := tmpl.Name()
		if name != templateName && strings.HasSuffix(name, ".html") {
			errmsgs[templateName] = append(errmsgs[templateName], fmt.Sprintf("%s: define %q: defined template's name cannot end in .html", templateName, name))
		}
	}
	if len(errmsgs[templateName]) > 0 {
		return nil, nil
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
	finalTemplate := template.New(templateName)
	slices.SortFunc(names, func(name1, name2 string) int {
		return -strings.Compare(name1, name2)
	})
	names = slices.Compact(names)
	for _, name := range names {
		tmpl := cache[name]
		if tmpl == nil {
			file, err := nbrew.FS.Open(path.Join(sitePrefix, "output/themes", name))
			if errors.Is(err, fs.ErrNotExist) {
				errmsgs[name] = append(errmsgs[name], fmt.Sprintf("%s calls nonexistent template %q", templateName, name))
				continue
			}
			if slices.Contains(callers, name) {
				errmsgs[callers[0]] = append(errmsgs[callers[0]], fmt.Sprintf(
					"calling %s ends in a circular reference: %s",
					callers[0],
					strings.Join(append(callers, name), " => "),
				))
				return nil, nil
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
			tmpl, err = nbrew.parseTemplate(sitePrefix, cache, errmsgs, append(callers, name), name, text)
			if err != nil {
				return nil, err
			}
			if tmpl == nil {
				continue
			}
			cache[name] = tmpl
		}
		for _, tmpl := range tmpl.Templates() {
			_, err = finalTemplate.AddParseTree(tmpl.Name(), tmpl.Tree)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: add %s: %w", templateName, name, tmpl.Name(), err)
			}
		}
	}
	if len(errmsgs) > 0 {
		return nil, nil
	}
	for _, tmpl := range primaryTemplates {
		_, err = finalTemplate.AddParseTree(tmpl.Name(), tmpl.Tree)
		if err != nil {
			return nil, fmt.Errorf("%s: add %s: %w", templateName, tmpl.Name(), err)
		}
	}
	return finalTemplate, nil
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

type TemplateErrors map[string][]string

func (templateErrors TemplateErrors) Error() string {
	names := make([]string, 0, len(templateErrors))
	for name := range templateErrors {
		names = append(names, name)
	}
	return fmt.Sprintf("the following templates have errors: %+v", names)
}

func (templateErrors TemplateErrors) List() []string {
	var list []string
	names := make([]string, 0, len(templateErrors))
	for name := range templateErrors {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, msg := range templateErrors[name] {
			list = append(list, msg)
		}
	}
	return list
}
