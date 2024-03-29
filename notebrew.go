package nb7

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bokwoon95/sq"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/sync/errgroup"
)

//go:embed embed static
var embedFS embed.FS

var rootFS fs.FS = embedFS

// Notebrew represents a notebrew instance.
type Notebrew struct {
	// FS is the file system associated with the notebrew instance.
	FS FS

	// DB is the database associated with the notebrew instance.
	DB *sql.DB

	// Dialect is dialect of the database. Only sqlite, postgres and mysql
	// databases are supported.
	Dialect string

	Scheme string // http:// | https://

	AdminDomain string // localhost:6444, example.com

	ContentDomain string // localhost:6444, example.com

	MultisiteMode string // subdomain | subdirectory

	// ErrorCode translates a database error into an dialect-specific error
	// code. If the error is not a database error or if no underlying
	// implementation is provided, ErrorCode returns an empty string.
	ErrorCode func(error) string

	FTS *FTS

	Logger *slog.Logger
}

func (nbrew *Notebrew) setSession(w http.ResponseWriter, r *http.Request, name string, value any) error {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(&value)
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	cookie := &http.Cookie{
		Path:     "/",
		Name:     name,
		Secure:   nbrew.Scheme == "https://",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if nbrew.DB == nil {
		cookie.Value = base64.URLEncoding.EncodeToString(buf.Bytes())
	} else {
		var sessionToken [8 + 16]byte
		binary.BigEndian.PutUint64(sessionToken[:8], uint64(time.Now().Unix()))
		_, err := rand.Read(sessionToken[8:])
		if err != nil {
			return fmt.Errorf("reading rand: %w", err)
		}
		var sessionTokenHash [8 + blake2b.Size256]byte
		checksum := blake2b.Sum256(sessionToken[8:])
		copy(sessionTokenHash[:8], sessionToken[:8])
		copy(sessionTokenHash[8:], checksum[:])
		_, err = sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "INSERT INTO session (session_token_hash, data) VALUES ({sessionTokenHash}, {data})",
			Values: []any{
				sq.BytesParam("sessionTokenHash", sessionTokenHash[:]),
				sq.StringParam("data", strings.TrimSpace(buf.String())),
			},
		})
		if err != nil {
			return fmt.Errorf("saving session: %w", err)
		}
		cookie.Value = strings.TrimLeft(hex.EncodeToString(sessionToken[:]), "0")
	}
	http.SetCookie(w, cookie)
	return nil
}

func (nbrew *Notebrew) getSession(r *http.Request, name string, valuePtr any) (ok bool, err error) {
	cookie, _ := r.Cookie(name)
	if cookie == nil {
		return false, nil
	}
	var dataBytes []byte
	if nbrew.DB == nil {
		dataBytes, err = base64.URLEncoding.DecodeString(cookie.Value)
		if err != nil {
			return false, nil
		}
	} else {
		sessionToken, err := hex.DecodeString(fmt.Sprintf("%048s", cookie.Value))
		if err != nil {
			return false, nil
		}
		var sessionTokenHash [8 + blake2b.Size256]byte
		checksum := blake2b.Sum256(sessionToken[8:])
		copy(sessionTokenHash[:8], sessionToken[:8])
		copy(sessionTokenHash[8:], checksum[:])
		createdAt := time.Unix(int64(binary.BigEndian.Uint64(sessionTokenHash[:8])), 0)
		if time.Now().Sub(createdAt) > 5*time.Minute {
			return false, nil
		}
		dataBytes, err = sq.FetchOneContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "SELECT {*} FROM session WHERE session_token_hash = {sessionTokenHash}",
			Values: []any{
				sq.BytesParam("sessionTokenHash", sessionTokenHash[:]),
			},
		}, func(row *sq.Row) []byte {
			return row.Bytes("data")
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
	}
	err = json.Unmarshal(dataBytes, valuePtr)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (nbrew *Notebrew) clearSession(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Path:     "/",
		Name:     name,
		Value:    "0",
		MaxAge:   -1,
		Secure:   nbrew.Scheme == "https://",
		HttpOnly: true,
	})
	cookie, _ := r.Cookie(name)
	if cookie == nil {
		return
	}
	sessionToken, err := hex.DecodeString(fmt.Sprintf("%048s", cookie.Value))
	if err != nil {
		return
	}
	var sessionTokenHash [8 + blake2b.Size256]byte
	checksum := blake2b.Sum256(sessionToken[8:])
	copy(sessionTokenHash[:8], sessionToken[:8])
	copy(sessionTokenHash[8:], checksum[:])
	_, err = sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
		Dialect: nbrew.Dialect,
		Format:  "DELETE FROM session WHERE session_token_hash = {sessionTokenHash}",
		Values: []any{
			sq.BytesParam("sessionTokenHash", sessionTokenHash[:]),
		},
	})
	if err != nil {
		logger, ok := r.Context().Value(loggerKey).(*slog.Logger)
		if !ok {
			logger = slog.Default()
		}
		logger.Error(err.Error())
	}
}

func getAuthenticationTokenHash(r *http.Request) []byte {
	var rawValue string
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, "Notebrew ") {
		rawValue = strings.TrimPrefix(header, "Notebrew ")
	} else {
		cookie, _ := r.Cookie("authentication")
		if cookie != nil {
			rawValue = cookie.Value
		}
	}
	if rawValue == "" {
		return nil
	}
	authenticationToken, err := hex.DecodeString(fmt.Sprintf("%048s", rawValue))
	if err != nil {
		return nil
	}
	var authenticationTokenHash [8 + blake2b.Size256]byte
	checksum := blake2b.Sum256(authenticationToken[8:])
	copy(authenticationTokenHash[:8], authenticationToken[:8])
	copy(authenticationTokenHash[8:], checksum[:])
	return authenticationTokenHash[:]
}

func hashToken(token []byte) []byte {
	var hashedToken [8 + blake2b.Size256]byte
	checksum := blake2b.Sum256(token[8:])
	copy(hashedToken[:8], token[:8])
	copy(hashedToken[8:], checksum[:])
	return hashedToken[:]
}

func (nbrew *Notebrew) IsKeyViolation(err error) bool {
	if err == nil || nbrew.ErrorCode == nil {
		return false
	}
	errcode := nbrew.ErrorCode(err)
	switch nbrew.Dialect {
	case "sqlite":
		return errcode == "1555" || errcode == "2067" // SQLITE_CONSTRAINT_PRIMARYKEY, SQLITE_CONSTRAINT_UNIQUE
	case "postgres":
		return errcode == "23505" // unique_violation
	case "mysql":
		return errcode == "1062" // ER_DUP_ENTRY
	case "sqlserver":
		return errcode == "2627"
	default:
		return false
	}
}

func (nbrew *Notebrew) IsForeignKeyViolation(err error) bool {
	if err == nil || nbrew.ErrorCode == nil {
		return false
	}
	errcode := nbrew.ErrorCode(err)
	switch nbrew.Dialect {
	case "sqlite":
		return errcode == "787" //  SQLITE_CONSTRAINT_FOREIGNKEY
	case "postgres":
		return errcode == "23503" // foreign_key_violation
	case "mysql":
		return errcode == "1216" // ER_NO_REFERENCED_ROW
	case "sqlserver":
		return errcode == "547"
	default:
		return false
	}
}

var base32Encoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

func NewID() [16]byte {
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], uint64(time.Now().Unix()))
	var id [16]byte
	copy(id[:5], timestamp[len(timestamp)-5:])
	_, err := rand.Read(id[5:])
	if err != nil {
		panic(err)
	}
	return id
}

var goldmarkMarkdown = func() goldmark.Markdown {
	md := goldmark.New()
	md.Parser().AddOptions(parser.WithAttribute())
	extension.Table.Extend(md)
	return md
}()

func stripMarkdownStyles(src []byte) string {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	var node ast.Node
	nodes := []ast.Node{goldmarkMarkdown.Parser().Parse(text.NewReader(src))}
	for len(nodes) > 0 {
		node, nodes = nodes[len(nodes)-1], nodes[:len(nodes)-1]
		if node == nil {
			continue
		}
		switch node := node.(type) {
		case *ast.Text:
			buf.Write(node.Text(src))
		}
		nodes = append(nodes, node.NextSibling(), node.FirstChild())
	}
	// Manually escape backslashes (goldmark may be able to do this,
	// investigate).
	var b strings.Builder
	str := buf.String()
	// Jump to the location of each backslash found in the string.
	for i := strings.IndexByte(str, '\\'); i >= 0; i = strings.IndexByte(str, '\\') {
		// Write everything in the string before the backslash.
		b.WriteString(str[:i])
		// If the backslash is the last character in the string, stop.
		if i == len(str)-1 {
			break
		}
		// Write the next character immediately following the backslash.
		char, width := utf8.DecodeRuneInString(str[i+1:])
		b.WriteRune(char)
		// Shrink the string to exclude what we have just written and continue
		// the search for the next backslash.
		str = str[i+1+width:]
	}
	// Write what is left of the string.
	b.WriteString(str)
	return b.String()
}

var uppercaseCharSet = map[rune]struct{}{
	'A': {}, 'B': {}, 'C': {}, 'D': {}, 'E': {}, 'F': {}, 'G': {}, 'H': {}, 'I': {},
	'J': {}, 'K': {}, 'L': {}, 'M': {}, 'N': {}, 'O': {}, 'P': {}, 'Q': {}, 'R': {},
	'S': {}, 'T': {}, 'U': {}, 'V': {}, 'W': {}, 'X': {}, 'Y': {}, 'Z': {},
}

var forbiddenCharSet = map[rune]struct{}{
	' ': {}, '!': {}, '"': {}, '#': {}, '$': {}, '%': {}, '&': {}, '\'': {},
	'(': {}, ')': {}, '*': {}, '+': {}, ',': {}, '/': {}, ':': {}, ';': {},
	'<': {}, '>': {}, '=': {}, '?': {}, '[': {}, ']': {}, '\\': {}, '^': {},
	'`': {}, '{': {}, '}': {}, '|': {}, '~': {},
}

var forbiddenNameSet = map[string]struct{}{
	"con": {}, "prn": {}, "aux": {}, "nul": {}, "com1": {}, "com2": {},
	"com3": {}, "com4": {}, "com5": {}, "com6": {}, "com7": {}, "com8": {},
	"com9": {}, "lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {},
	"lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

func urlSafe(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, char := range s {
		if utf8.RuneCountInString(b.String()) >= 80 {
			break
		}
		if char == ' ' {
			b.WriteRune('-')
			continue
		}
		if char == '-' || (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') {
			b.WriteRune(char)
			continue
		}
		if char >= 'A' && char <= 'Z' {
			b.WriteRune(unicode.ToLower(char))
			continue
		}
		if _, ok := forbiddenCharSet[char]; ok {
			continue
		}
		b.WriteRune(char)
	}
	return strings.Trim(b.String(), ".")
}

func getHost(r *http.Request) string {
	if r.Host == "127.0.0.1" {
		return "localhost"
	}
	if strings.HasPrefix(r.Host, "127.0.0.1:") {
		return "localhost" + strings.TrimPrefix(r.Host, "127.0.0.1:")
	}
	return r.Host
}

var (
	commonPasswordHashes = make(map[string]struct{})
	stylesCSS            string
	stylesCSSHash        string
	baselineJS           string
	baselineJSHash       string
	folderJS             string
	folderJSHash         string
)

func init() {
	// top-10000-passwords.txt
	file, err := rootFS.Open("embed/top-10000-passwords.txt")
	if err != nil {
		return
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	done := false
	for {
		if done {
			break
		}
		line, err := reader.ReadBytes('\n')
		done = err == io.EOF
		if err != nil && !done {
			panic(err)
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		hash := blake2b.Sum256([]byte(line))
		encodedHash := hex.EncodeToString(hash[:])
		commonPasswordHashes[encodedHash] = struct{}{}
	}
	// styles.css
	b, err := fs.ReadFile(rootFS, "static/styles.css")
	if err != nil {
		return
	}
	hash := sha256.Sum256(b)
	stylesCSS = string(b)
	stylesCSSHash = "'sha256-" + base64.StdEncoding.EncodeToString(hash[:]) + "'"
	// baseline.js
	b, err = fs.ReadFile(rootFS, "static/baseline.js")
	if err != nil {
		return
	}
	hash = sha256.Sum256(b)
	baselineJS = string(b)
	baselineJSHash = "'sha256-" + base64.StdEncoding.EncodeToString(hash[:]) + "'"
	// folder.js
	b, err = fs.ReadFile(rootFS, "static/folder.js")
	if err != nil {
		return
	}
	hash = sha256.Sum256(b)
	folderJS = string(b)
	folderJSHash = "'sha256-" + base64.StdEncoding.EncodeToString(hash[:]) + "'"
}

func IsCommonPassword(password []byte) bool {
	hash := blake2b.Sum256(password)
	encodedHash := hex.EncodeToString(hash[:])
	_, ok := commonPasswordHashes[encodedHash]
	return ok
}

type contextKey struct{}

var loggerKey = &contextKey{}

func getLogger(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return logger
	}
	return slog.Default()
}

var bufPool = sync.Pool{
	New: func() any { return &bytes.Buffer{} },
}

func getIP(r *http.Request) string {
	// Get IP from the X-REAL-IP header
	ip := r.Header.Get("X-REAL-IP")
	_, err := netip.ParseAddr(ip)
	if err == nil {
		return ip
	}
	// Get IP from X-FORWARDED-FOR header
	ips := r.Header.Get("X-FORWARDED-FOR")
	splitIps := strings.Split(ips, ",")
	for _, ip := range splitIps {
		_, err = netip.ParseAddr(ip)
		if err == nil {
			return ip
		}
	}
	// Get IP from RemoteAddr
	ip, _, err = net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	_, err = netip.ParseAddr(ip)
	if err == nil {
		return ip
	}
	return ""
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string, checkForGzipFallback bool) {
	if r.Method != "GET" {
		methodNotAllowed(w, r)
		return
	}

	var isGzippable bool
	ext := path.Ext(name)
	switch ext {
	// https://www.fastly.com/blog/new-gzip-settings-and-deciding-what-compress
	case ".html", ".css", ".js", ".md", ".txt", ".csv", ".tsv", ".json", ".xml", ".toml", ".yaml", ".yml", ".svg", ".ico", ".eot", ".otf", ".ttf":
		isGzippable = true
	case ".jpeg", ".jpg", ".png", ".gif", ".woff", ".woff2":
		isGzippable = false
	case ".webmanifest":
		isGzippable = true
	default:
		notFound(w, r)
		return
	}

	var isGzipped bool
	file, err := fsys.Open(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		if !isGzippable || !checkForGzipFallback {
			notFound(w, r)
			return
		}
		file, err = fsys.Open(name + ".gz")
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			notFound(w, r)
			return
		}
		isGzipped = true
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		getLogger(r.Context()).Error(err.Error())
		internalServerError(w, r, err)
		return
	}
	if fileInfo.IsDir() {
		notFound(w, r)
		return
	}

	if !isGzippable {
		fileSeeker, ok := file.(io.ReadSeeker)
		if ok {
			http.ServeContent(w, r, name, fileInfo.ModTime(), fileSeeker)
			return
		}
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)
		_, err = buf.ReadFrom(file)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		http.ServeContent(w, r, name, fileInfo.ModTime(), bytes.NewReader(buf.Bytes()))
		return
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	hasher := hashPool.Get().(hash.Hash)
	hasher.Reset()
	defer hashPool.Put(hasher)

	multiWriter := io.MultiWriter(buf, hasher)
	if isGzipped {
		_, err = io.Copy(multiWriter, file)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
	} else {
		gzipWriter := gzipPool.Get().(*gzip.Writer)
		gzipWriter.Reset(multiWriter)
		defer gzipPool.Put(gzipWriter)
		_, err = io.Copy(gzipWriter, file)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		err = gzipWriter.Close()
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
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

	if ext == ".webmanifest" {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("ETag", `"`+string(*dst)+`"`)
	http.ServeContent(w, r, name, fileInfo.ModTime(), bytes.NewReader(buf.Bytes()))
}

func getFileSize(fsys fs.FS, root string) (int64, error) {
	type Item struct {
		Path     string // relative to root
		DirEntry fs.DirEntry
	}
	fileInfo, err := fs.Stat(fsys, root)
	if err != nil {
		return 0, err
	}
	if !fileInfo.IsDir() {
		return fileInfo.Size(), nil
	}
	var size int64
	var item Item
	var items []Item
	dirEntries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return 0, err
	}
	for i := len(dirEntries) - 1; i >= 0; i-- {
		items = append(items, Item{
			Path:     dirEntries[i].Name(),
			DirEntry: dirEntries[i],
		})
	}
	for len(items) > 0 {
		item, items = items[len(items)-1], items[:len(items)-1]
		if !item.DirEntry.IsDir() {
			fileInfo, err = item.DirEntry.Info()
			if err != nil {
				return 0, fmt.Errorf("%s: %w", path.Join(root, item.Path), err)
			}
			size += fileInfo.Size()
			continue
		}
		dirEntries, err = fs.ReadDir(fsys, path.Join(root, item.Path))
		if err != nil {
			return 0, fmt.Errorf("%s: %w", path.Join(root, item.Path), err)
		}
		for i := len(dirEntries) - 1; i >= 0; i-- {
			items = append(items, Item{
				Path:     path.Join(item.Path, dirEntries[i].Name()),
				DirEntry: dirEntries[i],
			})
		}
	}
	return size, nil
}

func fileSizeToString(size int64) string {
	// https://yourbasic.org/golang/formatting-byte-size-to-human-readable-format/
	if size < 0 {
		return ""
	}
	const unit = 1000
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "kMGTPE"[exp])
}

var commonFuncMap = map[string]any{
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
}

func contentSiteURL(nbrew *Notebrew, sitePrefix string) string {
	if strings.Contains(sitePrefix, ".") {
		return "https://" + sitePrefix + "/"
	}
	if sitePrefix != "" {
		switch nbrew.MultisiteMode {
		case "subdomain":
			return nbrew.Scheme + strings.TrimPrefix(sitePrefix, "@") + "." + nbrew.ContentDomain + "/"
		case "subdirectory":
			return nbrew.Scheme + nbrew.ContentDomain + "/" + sitePrefix + "/"
		default:
			return ""
		}
	}
	return nbrew.Scheme + nbrew.ContentDomain + "/"
}

func neatenURL(s string) string {
	if strings.HasPrefix(s, "https://") {
		return strings.TrimSuffix(strings.TrimPrefix(s, "https://"), "/")
	}
	return strings.TrimSuffix(strings.TrimPrefix(s, "http://"), "/")
}

type Post = struct {
	URL       string
	Category  string
	Name      string
	Title     string
	Preview   string
	Content   template.HTML
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (nbrew *Notebrew) getPosts(ctx context.Context, sitePrefix, category string) ([]Post, error) {
	siteURL := nbrew.Scheme + nbrew.ContentDomain
	if strings.Contains(sitePrefix, ".") {
		siteURL = "https://" + sitePrefix
	} else if sitePrefix != "" {
		switch nbrew.MultisiteMode {
		case "subdomain":
			siteURL = nbrew.Scheme + strings.TrimPrefix(sitePrefix, "@") + "." + nbrew.ContentDomain
		case "subdirectory":
			siteURL = nbrew.Scheme + nbrew.ContentDomain + "/" + sitePrefix
		}
	}
	if category != urlSafe(category) {
		return nil, fs.ErrNotExist
	}
	fileInfo, err := fs.Stat(nbrew.FS, path.Join(sitePrefix, "posts", category))
	if err != nil {
		return nil, err
	}
	if !fileInfo.IsDir() {
		return nil, nil
	}
	dirEntries, err := nbrew.FS.ReadDir(path.Join(sitePrefix, "posts", category))
	if err != nil {
		return nil, err
	}
	var posts []Post
	for _, dirEntry := range dirEntries {
		err := ctx.Err()
		if err != nil {
			return nil, err
		}
		if dirEntry.IsDir() {
			continue
		}
		name := dirEntry.Name()
		ext := path.Ext(name)
		if ext != ".md" && ext != ".txt" {
			continue
		}
		fileInfo, err := dirEntry.Info()
		if err != nil {
			return nil, err
		}
		var createdAt time.Time
		prefix, _, ok := strings.Cut(name, "-")
		if ok && len(prefix) > 0 && len(prefix) <= 8 {
			b, _ := base32Encoding.DecodeString(fmt.Sprintf("%08s", prefix))
			if len(b) == 5 {
				var timestamp [8]byte
				copy(timestamp[len(timestamp)-5:], b)
				createdAt = time.Unix(int64(binary.BigEndian.Uint64(timestamp[:])), 0)
			}
		}
		post := Post{
			URL:       siteURL + "/" + path.Join("posts", category, strings.TrimSuffix(name, path.Ext(name))) + "/",
			Category:  category,
			Name:      name,
			CreatedAt: createdAt,
			UpdatedAt: fileInfo.ModTime(),
		}
		posts = append(posts, post)
	}
	slices.SortFunc(posts, func(p1, p2 Post) int {
		if p1.CreatedAt.Equal(p2.CreatedAt) {
			return 0
		}
		if p1.CreatedAt.Before(p2.CreatedAt) {
			return 1
		}
		return -1
	})
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i := range posts {
		post := &posts[i]
		g.Go(func() error {
			file, err := nbrew.FS.Open(path.Join(sitePrefix, "posts", post.Category, post.Name))
			if err != nil {
				return err
			}
			reader := bufio.NewReader(file)
			proceed := true
			for proceed {
				err := ctx.Err()
				if err != nil {
					return err
				}
				line, err := reader.ReadBytes('\n')
				if err != nil {
					proceed = false
				}
				line = bytes.TrimSpace(line)
				if len(line) == 0 {
					continue
				}
				if post.Title == "" {
					post.Title = stripMarkdownStyles(line)
					continue
				}
				post.Preview = stripMarkdownStyles(line)
				break
			}
			return nil
		})
	}
	err = g.Wait()
	if err != nil {
		return nil, err
	}
	return posts, nil
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+string(*dst)+`"`)
	http.ServeContent(w, r, "", modtime, bytes.NewReader(buf.Bytes()))
}

func contentSecurityPolicy(w http.ResponseWriter, cdnBaseURL string, allowCaptcha bool) {
	var b strings.Builder
	// default-src
	b.WriteString("default-src 'none';")
	// script-src
	b.WriteString(" script-src 'self' 'unsafe-hashes' " + baselineJSHash + " " + folderJSHash)
	if cdnBaseURL != "" {
		b.WriteString(" " + cdnBaseURL)
	}
	if allowCaptcha {
		b.WriteString(" https://hcaptcha.com https://*.hcaptcha.com")
	}
	b.WriteString(";")
	// connect-src
	b.WriteString(" connect-src 'self'")
	if allowCaptcha {
		b.WriteString(" https://hcaptcha.com https://*.hcaptcha.com")
	}
	b.WriteString(";")
	// img-src
	b.WriteString(" img-src 'self' data:")
	if cdnBaseURL != "" {
		b.WriteString(" " + cdnBaseURL)
	}
	b.WriteString(";")
	// style-src
	b.WriteString(" style-src 'self' 'unsafe-inline'")
	if cdnBaseURL != "" {
		b.WriteString(" " + cdnBaseURL)
	}
	if allowCaptcha {
		b.WriteString(" https://hcaptcha.com https://*.hcaptcha.com")
	}
	b.WriteString(";")
	// base-uri
	b.WriteString(" base-uri 'self';")
	// form-action
	b.WriteString(" form-action 'self';")
	// manifest-src
	b.WriteString(" manifest-src 'self';")
	// frame-src
	if allowCaptcha {
		b.WriteString(" frame-src https://hcaptcha.com https://*.hcaptcha.com;")
	}
	w.Header().Set("Content-Security-Policy", b.String())
}

type JSONHandler struct {
	stdoutHandler slog.Handler
	stderrHandler slog.Handler
}

func NewJSONHandler(stdout io.Writer, stderr io.Writer, opts *slog.HandlerOptions) *JSONHandler {
	return &JSONHandler{
		stdoutHandler: slog.NewJSONHandler(stdout, opts),
		stderrHandler: slog.NewJSONHandler(stderr, opts),
	}
}

func (h *JSONHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.stderrHandler.Enabled(ctx, level)
}

func (h *JSONHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level == slog.LevelError {
		return h.stderrHandler.Handle(ctx, record)
	}
	return h.stdoutHandler.Handle(ctx, record)
}

func (h *JSONHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &JSONHandler{
		stdoutHandler: h.stdoutHandler.WithAttrs(attrs),
		stderrHandler: h.stderrHandler.WithAttrs(attrs),
	}
}

func (h *JSONHandler) WithGroup(name string) slog.Handler {
	return &JSONHandler{
		stdoutHandler: h.stdoutHandler.WithGroup(name),
		stderrHandler: h.stderrHandler.WithGroup(name),
	}
}
