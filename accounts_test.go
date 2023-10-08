package nb7

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/bokwoon95/nb7/internal/testutil"
	"github.com/bokwoon95/sq"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/sync/errgroup"
)

func Test_login(t *testing.T) {
	type Response struct {
		Status              Error              `json:"status"`
		Username            string             `json:"username,omitempty"`
		Errors              map[string][]Error `json:"errors,omitempty"`
		AuthenticationToken string             `json:"authenticationToken,omitempty"`
		Redirect            string             `json:"redirect,omitempty"`
	}
	type TestTable struct {
		description  string
		request      func(t *testing.T, nbrew *Notebrew) *http.Request
		wantResponse Response
	}

	tests := []TestTable{{
		description: "GET user already logged in",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "adalynn", "adalynn@email.com", "password123")
			r, _ := http.NewRequest("GET", "/admin/login/", nil)
			r.Header.Set("Authorization", "Notebrew "+generateAuthenticationToken(t, nbrew, "adalynn"))
			return r
		},
		wantResponse: Response{
			Status: ErrAlreadyAuthenticated,
		},
	}, {
		description: "POST user already logged in",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "bailey", "bailey@email.com", "password123")
			r, _ := http.NewRequest("POST", "/admin/login/", strings.NewReader("username=bailey&password=password123"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Authorization", "Notebrew "+generateAuthenticationToken(t, nbrew, "bailey"))
			return r
		},
		wantResponse: Response{
			Status:   ErrAlreadyAuthenticated,
			Username: "bailey",
		},
	}, {
		description: "redirect is /admin/notes/",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("GET", "/admin/login/?redirect=/admin/notes/", nil)
			return r
		},
		wantResponse: Response{
			Status:   Success,
			Redirect: "/admin/notes/",
		},
	}, {
		description: "redirect is /admin/",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("GET", "/admin/login/?redirect=/admin/", nil)
			return r
		},
		wantResponse: Response{
			Status: Success,
		},
	}, {
		description: "redirect is /admin/login/",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("GET", "/admin/login/?redirect=/admin/login/", nil)
			return r
		},
		wantResponse: Response{
			Status: Success,
		},
	}, {
		description: "redirect is /foo/bar/baz/",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("GET", "/admin/login/?redirect=/foo/bar/baz/", nil)
			return r
		},
		wantResponse: Response{
			Status: Success,
		},
	}, {
		description: "success",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "jude", "jude@email.com", "password123")
			r, _ := http.NewRequest("POST", "/admin/login/", strings.NewReader("username=jude&password=password123"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
		wantResponse: Response{
			Status:              Success,
			Username:            "jude",
			AuthenticationToken: "<expected>",
		},
	}, {
		description: "@username and json",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "callum", "callum@email.com", "password123")
			r, _ := http.NewRequest("POST", "/admin/login/", strings.NewReader(`{"username":"@callum","password":"password123"}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		},
		wantResponse: Response{
			Status:              Success,
			Username:            "callum",
			AuthenticationToken: "<expected>",
		},
	}, {
		description: "username and password empty",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "iris", "iris@email.com", "password123")
			r, _ := http.NewRequest("POST", "/admin/login/", strings.NewReader("username=&password="))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
		wantResponse: Response{
			Status: ErrValidationFailed,
			Errors: map[string][]Error{
				"username": {ErrRequired},
				"password": {ErrRequired},
			},
		},
	}, {
		description: "username is email, redirect is /admin/createcategory/?type=posts",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "christopher", "christopher@email.com", "password123")
			r, _ := http.NewRequest("POST", "/admin/login/", strings.NewReader("username=christopher@email.com&password=password123&redirect=/admin/createcategory/?type=posts"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
		wantResponse: Response{
			Status:              Success,
			Username:            "christopher@email.com",
			AuthenticationToken: "<expected>",
			Redirect:            "/admin/createcategory/?type=posts",
		},
	}, {
		description: "username is username, redirect is /admin/notes/foo.md",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "jacob", "jacob@email.com", "password123")
			r, _ := http.NewRequest("POST", "/admin/login/", strings.NewReader("username=jacob&password=password123&redirect=/admin/notes/foo.md"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
		wantResponse: Response{
			Status:              Success,
			Username:            "jacob",
			AuthenticationToken: "<expected>",
			Redirect:            "/admin/notes/foo.md",
		},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.description, func(t *testing.T) {
			t.Parallel()
			g, ctx := errgroup.WithContext(context.Background())
			for dialect, db := range databases {
				nbrew := &Notebrew{
					Dialect:   dialect,
					DB:        db,
					FS:        testutil.NewFS(nil),
					ErrorCode: errorCodeFuncs[dialect],
				}
				g.Go(func() error {
					w := httptest.NewRecorder()
					r := tt.request(t, nbrew)
					r.Header.Set("Accept", "application/json")
					nbrew.login(w, r.WithContext(ctx), getIP(r))
					if ctx.Err() != nil {
						return nil
					}
					var gotResponse Response
					err := json.Unmarshal(w.Body.Bytes(), &gotResponse)
					if err != nil {
						return fmt.Errorf("[%s] %s %v\nResponse Body: %s", nbrew.Dialect, testutil.Callers(), err, w.Body.String())
					}
					if gotResponse.Status == "" {
						return fmt.Errorf("[%s] %s status is empty", nbrew.Dialect, testutil.Callers())
					}
					if tt.wantResponse.AuthenticationToken == "" && gotResponse.AuthenticationToken != "" {
						return fmt.Errorf("[%s] %s did not expect authentication token but received one", nbrew.Dialect, testutil.Callers())
					}
					if tt.wantResponse.AuthenticationToken == "<expected>" {
						if gotResponse.AuthenticationToken == "" {
							return fmt.Errorf("[%s] %s expected authentication token but did not receive one", nbrew.Dialect, testutil.Callers())
						}
						authenticationToken, err := hex.DecodeString(fmt.Sprintf("%048s", gotResponse.AuthenticationToken))
						if err != nil {
							return fmt.Errorf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
						}
						authenticationTokenHash := make([]byte, 8+blake2b.Size256)
						checksum := blake2b.Sum256(authenticationToken[8:])
						copy(authenticationTokenHash[:8], authenticationToken[:8])
						copy(authenticationTokenHash[8:], checksum[:])
						exists, err := sq.FetchExists(nbrew.DB, sq.CustomQuery{
							Dialect: nbrew.Dialect,
							Format:  "SELECT 1 FROM authentication WHERE authentication_token_hash = {}",
							Values:  []any{authenticationTokenHash},
						})
						if err != nil {
							return fmt.Errorf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
						}
						if !exists {
							return fmt.Errorf("[%s] %s authentication token was present in response but its hash was not saved to the database", nbrew.Dialect, testutil.Callers())
						}
					}
					// Zero out the AuthenticationToken field because it is
					// randomly generated and we don't want to compare on it.
					wantResponse := tt.wantResponse
					wantResponse.AuthenticationToken = ""
					gotResponse.AuthenticationToken = ""
					if diff := testutil.Diff(gotResponse, wantResponse); diff != "" {
						return fmt.Errorf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), diff)
					}
					return nil
				})
			}
			err := g.Wait()
			if err != nil {
				t.Error(err)
			}
		})
	}
}

func Test_logout(t *testing.T) {
	g, ctx := errgroup.WithContext(context.Background())
	for dialect, db := range databases {
		nbrew := &Notebrew{
			Dialect:   dialect,
			DB:        db,
			FS:        testutil.NewFS(nil),
			ErrorCode: errorCodeFuncs[dialect],
		}
		g.Go(func() error {
			createUser(t, nbrew, "kate", "kate@email.com", "password123")

			w := httptest.NewRecorder()
			r, _ := http.NewRequest("POST", "/admin/login/", strings.NewReader(`{"username":"kate","password":"password123"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json")
			nbrew.login(w, r.WithContext(ctx), getIP(r))
			if ctx.Err() != nil {
				return nil
			}
			var response map[string]any
			err := json.Unmarshal(w.Body.Bytes(), &response)
			if err != nil {
				return fmt.Errorf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
			}
			authenticationToken := response["authenticationToken"].(string)

			w = httptest.NewRecorder()
			r, _ = http.NewRequest("POST", "/admin/logout/", nil)
			r.Header.Set("Authorization", "Notebrew "+authenticationToken)
			nbrew.logout(w, r.WithContext(ctx), "")
			if ctx.Err() != nil {
				return nil
			}
			exists, err := sq.FetchExistsContext(ctx, nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format: "SELECT 1" +
					" FROM users" +
					" JOIN authentication ON authentication.user_id = users.user_id" +
					" WHERE users.username = 'kate'",
			})
			if err != nil {
				return fmt.Errorf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
			}
			if exists {
				return fmt.Errorf("[%s] %s user was not logged out", nbrew.Dialect, testutil.Callers())
			}
			return nil
		})
	}
	err := g.Wait()
	if err != nil {
		t.Error(err)
	}
}

func Test_resetpassword_invalidTokenBadRequest(t *testing.T) {
	type TestTable struct {
		description string
		request     func(t *testing.T, nbrew *Notebrew) *http.Request
	}

	tests := []TestTable{{
		description: "GET without token",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("GET", "/admin/resetpassword/", nil)
			return r
		},
	}, {
		description: "GET with non-hexadecimal token",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("GET", "/admin/resetpassword/?token=abcdefghijklmnop", nil)
			return r
		},
	}, {
		description: "GET with nonexistent token",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("GET", "/admin/resetpassword/?token=64fd5f583e9ac639a2140905857e3440c2b16c59", nil)
			return r
		},
	}, {
		description: "POST without token",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("POST", "/admin/resetpassword/", strings.NewReader("password=newpassword&confirmPassword=newpassword"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
	}, {
		description: "POST with non-hexadecimal token",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("POST", "/admin/resetpassword/", strings.NewReader("password=newpassword&confirmPassword=newpassword&token=abcdefghijklmnop"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
	}, {
		description: "POST with nonexistent token",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			r, _ := http.NewRequest("POST", "/admin/resetpassword/", strings.NewReader("password=newpassword&confirmPassword=newpassword&token=64fd5f583e9ac639a2140905857e3440c2b16c59"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.description, func(t *testing.T) {
			t.Parallel()
			g, ctx := errgroup.WithContext(context.Background())
			for dialect, db := range databases {
				nbrew := &Notebrew{
					Dialect:   dialect,
					DB:        db,
					FS:        testutil.NewFS(nil),
					ErrorCode: errorCodeFuncs[dialect],
				}
				g.Go(func() error {
					w := httptest.NewRecorder()
					r := tt.request(t, nbrew)
					r.Header.Set("Accept", "application/json")
					nbrew.resetpassword(w, r.WithContext(ctx), "")
					if ctx.Err() != nil {
						return nil
					}
					if diff := testutil.Diff(w.Code, http.StatusBadRequest); diff != "" {
						return fmt.Errorf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), diff)
					}
					return nil
				})
			}
			err := g.Wait()
			if err != nil {
				t.Error(err)
			}
		})
	}
}

func Test_resetpassword(t *testing.T) {
	type Response struct {
		Errors url.Values `json:"errors,omitempty"`
	}
	type TestTable struct {
		description  string
		request      func(t *testing.T, nbrew *Notebrew) *http.Request
		wantResponse Response
	}

	tests := []TestTable{{
		description: "password too short",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "trevor", "trevor@email.com", "password123")
			r, _ := http.NewRequest("POST", "/admin/resetpassword/", strings.NewReader(url.Values{
				"token":           []string{generateResetToken(t, nbrew, "trevor")},
				"password":        []string{"123456"},
				"confirmPassword": []string{"123456"},
			}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
		wantResponse: Response{
			Errors: url.Values{
				"": []string{"Password must be at least 8 characters"},
			},
		},
	}, {
		description: "password too common",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "august", "auguest@email.com", "password123")
			r, _ := http.NewRequest("POST", "/admin/resetpassword/", strings.NewReader(url.Values{
				"token":           []string{generateResetToken(t, nbrew, "august")},
				"password":        []string{"12345678"},
				"confirmPassword": []string{"12345678"},
			}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
		wantResponse: Response{
			Errors: url.Values{
				"": []string{"Password is too common."},
			},
		},
	}, {
		description: "passwords don't match",
		request: func(t *testing.T, nbrew *Notebrew) *http.Request {
			createUser(t, nbrew, "finn", "finn@email.com", "password123")
			r, _ := http.NewRequest("POST", "/admin/resetpassword/", strings.NewReader(url.Values{
				"token":           []string{generateResetToken(t, nbrew, "finn")},
				"password":        []string{"Hunter2!"},
				"confirmPassword": []string{"Hunter3!"},
			}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
		wantResponse: Response{
			Errors: url.Values{
				"": []string{"Passwords do not match"},
			},
		},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.description, func(t *testing.T) {
			t.Parallel()
			g, ctx := errgroup.WithContext(context.Background())
			for dialect, db := range databases {
				nbrew := &Notebrew{
					Dialect:   dialect,
					DB:        db,
					FS:        testutil.NewFS(nil),
					ErrorCode: errorCodeFuncs[dialect],
				}
				g.Go(func() error {
					w := httptest.NewRecorder()
					r := tt.request(t, nbrew)
					r.Header.Set("Accept", "application/json")
					nbrew.resetpassword(w, r.WithContext(ctx), "")
					if ctx.Err() != nil {
						return nil
					}
					if diff := testutil.Diff(w.Code, http.StatusOK); diff != "" {
						return fmt.Errorf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), diff)
					}
					var gotResponse Response
					err := json.Unmarshal(w.Body.Bytes(), &gotResponse)
					if err != nil {
						return fmt.Errorf("[%s] %s %v\nResponse Body: %s", nbrew.Dialect, testutil.Callers(), err, w.Body.String())
					}
					if diff := testutil.Diff(gotResponse, tt.wantResponse); diff != "" {
						return fmt.Errorf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), diff)
					}
					return nil
				})
			}
			err := g.Wait()
			if err != nil {
				t.Error(err)
			}
		})
	}
}

func generateResetToken(t *testing.T, nbrew *Notebrew, username string) string {
	var resetToken [8 + 16]byte
	binary.BigEndian.PutUint64(resetToken[:8], uint64(time.Now().Unix()))
	_, err := rand.Read(resetToken[8:])
	if err != nil {
		t.Fatal(testutil.Callers(), err)
	}
	checksum := blake2b.Sum256(resetToken[8:])
	var resetTokenHash [8 + blake2b.Size256]byte
	copy(resetTokenHash[:8], resetToken[:8])
	copy(resetTokenHash[8:], checksum[:])
	_, err = sq.Exec(nbrew.DB, sq.CustomQuery{
		Dialect: nbrew.Dialect,
		Format:  "UPDATE users SET reset_token_hash = {resetTokenHash} WHERE username = {username}",
		Values: []any{
			sq.BytesParam("resetTokenHash", resetTokenHash[:]),
			sq.StringParam("username", username),
		},
	})
	if err != nil {
		t.Fatal(testutil.Callers(), err)
	}
	return strings.TrimLeft(hex.EncodeToString(resetToken[:]), "0")
}

func createUser(t *testing.T, nbrew *Notebrew, username, email string, password string) {
	siteID := NewID()
	userID := NewID()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
	}
	tx, err := nbrew.DB.Begin()
	if err != nil {
		t.Fatalf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
	}
	defer tx.Rollback()
	_, err = sq.Exec(tx, sq.CustomQuery{
		Dialect: nbrew.Dialect,
		Format:  "INSERT INTO site (site_id, site_name) VALUES ({siteID}, {siteName})",
		Values: []any{
			sq.UUIDParam("siteID", siteID),
			sq.StringParam("siteName", username),
		},
	})
	if err != nil {
		t.Fatalf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
	}
	_, err = sq.Exec(tx, sq.CustomQuery{
		Dialect: nbrew.Dialect,
		Format: "INSERT INTO users (user_id, username, email, password_hash)" +
			" VALUES ({userID}, {username}, {email}, {passwordHash})",
		Values: []any{
			sq.UUIDParam("userID", userID),
			sq.StringParam("username", username),
			sq.StringParam("email", email),
			sq.StringParam("passwordHash", string(passwordHash)),
		},
	})
	if err != nil {
		t.Fatalf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
	}
	_, err = sq.Exec(tx, sq.CustomQuery{
		Dialect: nbrew.Dialect,
		Format:  "INSERT INTO site_user (site_id, user_id) VALUES ({siteID}, {userID})",
		Values: []any{
			sq.UUIDParam("siteID", siteID),
			sq.UUIDParam("userID", userID),
		},
	})
	if err != nil {
		t.Fatalf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
	}
	err = tx.Commit()
	if err != nil {
		t.Fatalf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
	}
	err = nbrew.FS.Mkdir("@"+username, 0755)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		t.Fatalf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
	}
	dirs := []string{
		"notes",
		"output",
		"output/images",
		"output/themes",
		"pages",
		"posts",
		"system",
	}
	for _, dir := range dirs {
		err = nbrew.FS.Mkdir(path.Join("@"+username, dir), 0755)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			t.Fatalf("[%s] %s %v", nbrew.Dialect, testutil.Callers(), err)
		}
	}
}

func generateAuthenticationToken(t *testing.T, nbrew *Notebrew, username string) string {
	var authenticationToken [8 + 16]byte
	binary.BigEndian.PutUint64(authenticationToken[:8], uint64(time.Now().Unix()))
	_, err := rand.Read(authenticationToken[8:])
	if err != nil {
		t.Fatal(testutil.Callers(), err)
	}
	var authenticationTokenHash [8 + blake2b.Size256]byte
	checksum := blake2b.Sum256(authenticationToken[8:])
	copy(authenticationTokenHash[:8], authenticationToken[:8])
	copy(authenticationTokenHash[8:], checksum[:])
	_, err = sq.Exec(nbrew.DB, sq.CustomQuery{
		Dialect: nbrew.Dialect,
		Format: "INSERT INTO authentication (authentication_token_hash, user_id)" +
			" VALUES ({authenticationTokenHash}, (SELECT user_id FROM users WHERE username = {username}))",
		Values: []any{
			sq.BytesParam("authenticationTokenHash", authenticationTokenHash[:]),
			sq.StringParam("username", username),
		},
	})
	if err != nil {
		t.Fatal(testutil.Callers(), err)
	}
	return strings.TrimLeft(hex.EncodeToString(authenticationToken[:]), "0")
}
