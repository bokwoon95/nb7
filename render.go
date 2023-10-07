package nb7

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"text/template/parse"
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
	// notebrew.com/@bokwoon/
	// ?after=xxxx&count=25
	// The main problem is that posts cannot be efficiently paginated (need to seek from the start everytime)
	// So we need to store status updates in the database, instead of the filesystem
	// notebrew.com/admin/
	// notebrew.com/user/bokwoon
	// notebrew.com/status/
	// notebrew.com/image/
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

// example.com/admin/
// example.com/@bokwoon/

func (renderer *Renderer) RenderPage(w io.Writer, name string, content []byte) error {
	return nil
}

func (renderer *Renderer) RenderPost(w io.Writer, content []byte) error {
	return nil
}

func (renderer *Renderer) RenderPostIndex(w io.Writer) error {
	return nil
}

func (renderer *Renderer) parse(templateName, templateText string, callers []string) (*template.Template, error) {
	primaryTemplate, err := template.New(templateName).Funcs(renderer.funcMap).Parse(templateText)
	if err != nil {
		renderer.mu.Lock()
		renderer.errmsgs[templateName] = append(renderer.errmsgs[templateName], strings.TrimSpace(strings.TrimPrefix(err.Error(), "template:")))
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

func (renderError RenderError) Errors() []string {
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
