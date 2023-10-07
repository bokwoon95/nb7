package nb7

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/url"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"text/template/parse"
	"time"

	"golang.org/x/sync/errgroup"
)

type TemplateParser struct {
	nbrew      *Notebrew
	sitePrefix string
	mu         *sync.RWMutex // protects cache and errmsgs
	cache      map[string]*template.Template
	errmsgs    map[string][]string
	funcMap    map[string]any
	ctx        context.Context
}

// createpost
// updatepost
// deletepost
// createpage
// updatepage
// regenerateSite

func NewTemplateParser(ctx context.Context, nbrew *Notebrew, sitePrefix string) *TemplateParser {
	parser := &TemplateParser{
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
			// TODO: cache the output of each call to getPosts for each category.
			return nbrew.getPosts(ctx, sitePrefix, category)
		},
	}
	return parser
}

func (parser *TemplateParser) Parse(templateText string) (*template.Template, error) {
	return parser.parse("", templateText, nil)
}

func (parser *TemplateParser) parse(templateName, templateText string, callers []string) (*template.Template, error) {
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
		err := parser.ctx.Err()
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
			tmpl, err = parser.parse(name, text, append(callers, name))
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

func (nbrew *Notebrew) RegenerateSite(ctx context.Context, sitePrefix string) error {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	templateParser := NewTemplateParser(ctx, nbrew, sitePrefix)

	file, err := nbrew.FS.Open(path.Join(sitePrefix, "output/themes/posts.html"))
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
	postsTmpl, err := templateParser.Parse(b.String())
	if err != nil {
		return err
	}
	err = MkdirAll(nbrew.FS, path.Join(sitePrefix, "output/posts"), 0755)
	if err != nil {
		return err
	}
	readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, "output/posts/index.html"), 0644)
	if err != nil {
		return err
	}
	pipeReader, pipeWriter := io.Pipe()
	ch := make(chan error, 1)
	go func() {
		_, err := readerFrom.ReadFrom(pipeReader)
		ch <- err
	}()
	err = postsTmpl.Execute(pipeWriter, nil)
	if err != nil {
		return err
	}
	err = pipeWriter.Close()
	if err != nil {
		return err
	}
	err = <-ch
	if err != nil {
		return err
	}

	file, err = nbrew.FS.Open(path.Join(sitePrefix, "output/themes/post.html"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		file, err = rootFS.Open("static/post.html")
		if err != nil {
			return err
		}
	}
	fileInfo, err = file.Stat()
	if err != nil {
		return err
	}
	b.Reset()
	b.Grow(int(fileInfo.Size()))
	_, err = io.Copy(&b, file)
	if err != nil {
		return err
	}
	postTmpl, err := templateParser.Parse(b.String())
	if err != nil {
		return err
	}
	_ = postTmpl
	fs.WalkDir(nbrew.FS, path.Join(sitePrefix, "posts"), func(filePath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		segments := strings.Split(strings.Trim(filePath, "/"), "/")
		if d.IsDir() {
			if len(segments) > 1 {
				return fs.SkipDir
			}
			if len(segments) == 1 {
				category := segments[0]
				err = MkdirAll(nbrew.FS, path.Join(sitePrefix, "output/posts", category), 0755)
				if err != nil {
					return err
				}
			}
			return nil
		}
		ext := path.Ext(filePath)
		if ext != ".md" && ext != ".txt" {
			return nil
		}
		g.Go(func() error {
			buf := bufPool.Get().(*bytes.Buffer)
			buf.Reset()
			defer bufPool.Put(buf)
			var category, name string
			if len(segments) == 3 {
				category, name = segments[1], strings.TrimSuffix(segments[2], ext)
			} else {
				name = strings.TrimSuffix(segments[1], ext)
			}
			var creationDate time.Time
			prefix, _, ok := strings.Cut(name, "-")
			if ok && len(prefix) > 0 && len(prefix) <= 8 {
				b, _ := base32Encoding.DecodeString(fmt.Sprintf("%08s", prefix))
				if len(b) == 5 {
					var timestamp [8]byte
					copy(timestamp[len(timestamp)-5:], b)
					creationDate = time.Unix(int64(binary.BigEndian.Uint64(timestamp[:])), 0)
				}
			}
			_ = creationDate
			file, err := nbrew.FS.Open(path.Join(sitePrefix, "posts", filePath))
			if err != nil {
				return err
			}
			_, err = buf.ReadFrom(file)
			if err != nil {
				return err
			}
			var title string
			var line []byte
			remainder := buf.Bytes()
			for len(remainder) > 0 {
				line, remainder, _ = bytes.Cut(remainder, []byte("\n"))
				line = bytes.TrimSpace(line)
				if len(line) == 0 {
					continue
				}
				var b strings.Builder
				stripMarkdownStyles(&b, line)
				title = b.String()
				break
			}
			_ = title
			fileInfo, err := d.Info()
			if err != nil {
				return err
			}
			_ = fileInfo.ModTime()
			readerFrom, err := nbrew.FS.OpenReaderFrom(path.Join(sitePrefix, "output/posts", category, name, "index.html"), 0644)
			if err != nil {
				return err
			}
			_ = readerFrom
			return nil
		})
		return nil
	})

	// posts
	// pages
	return g.Wait()
}
