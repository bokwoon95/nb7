package nb7

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bokwoon95/sq"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/blake2b"
)

func (nbrew *Notebrew) resetpassword(w http.ResponseWriter, r *http.Request, ip string) {
	type Request struct {
		Email           string `json:"email,omitempty"`
		ResetToken      string `json:"resetToken,omitempty"`
		Password        string `json:"password,omitempty"`
		ConfirmPassword string `json:"confirmPassword,omitempty"`
	}
	type Response struct {
		Status           Error              `json:"status,omitempty"`
		Email            string             `json:"email,omitempty"`
		ResetToken       string             `json:"resetToken,omitempty"`
		Errors           map[string][]Error `json:"errors,omitempty"`
		MailSendingError string             `json:"mailSendingError,omitempty"`
	}
	type SmtpSettings struct {
		Username string
		Password string
		Host     string
		Port     string
	}

	if nbrew.DB == nil {
		notFound(w, r)
		return
	}

	isAuthenticated := func() bool {
		authenticationTokenHash := getAuthenticationTokenHash(r)
		if authenticationTokenHash == nil {
			return false
		}
		exists, err := sq.FetchExistsContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "SELECT 1 FROM authentication WHERE authentication_token_hash = {authenticationTokenHash}",
			Values: []any{
				sq.BytesParam("authenticationTokenHash", authenticationTokenHash),
			},
		})
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
		}
		return exists
	}

	hashAndValidateResetToken := func(resetToken string) (resetTokenHash []byte, expired bool, err error) {
		if len(resetToken) > 48 {
			return nil, false, nil
		}
		b, err := hex.DecodeString(fmt.Sprintf("%048s", resetToken))
		if err != nil {
			return nil, false, nil
		}
		checksum := blake2b.Sum256(b[8:])
		resetTokenHash = make([]byte, 8+blake2b.Size256)
		copy(resetTokenHash[:8], b[:8])
		copy(resetTokenHash[8:], checksum[:])
		exists, err := sq.FetchExistsContext(r.Context(), nbrew.DB, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format:  "SELECT 1 FROM users WHERE reset_token_hash = {resetTokenHash}",
			Values: []any{
				sq.BytesParam("resetTokenHash", resetTokenHash),
			},
		})
		if err != nil {
			return nil, false, err
		}
		if !exists {
			return nil, false, nil
		}
		issuedAt := time.Unix(int64(binary.BigEndian.Uint64(resetTokenHash[:8])), 0)
		if time.Now().Sub(issuedAt) > 20*time.Minute {
			return resetTokenHash, true, nil
		}
		return resetTokenHash, false, nil
	}

	getSmtpSettings := func() (smtpSettings *SmtpSettings, isDisabled, isMisconfigured bool, err error) {
		b, err := fs.ReadFile(nbrew.FS, "config/mailer.txt")
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, true, false, nil
			}
			return nil, false, false, err
		}
		smtpURL := string(bytes.TrimSpace(b))
		if strings.HasPrefix(smtpURL, "file:") {
			filename := strings.TrimPrefix(strings.TrimPrefix(smtpURL, "file:"), "//")
			b, err := os.ReadFile(filename)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil, false, true, nil
				}
				return nil, false, true, err
			}
			smtpURL = string(bytes.TrimSpace(b))
		}
		smtpSettings = &SmtpSettings{}
		uri, err := url.Parse(smtpURL)
		if err != nil {
			return nil, false, true, nil
		}
		if uri.Scheme != "smtp" {
			return nil, false, true, nil
		}
		if uri.User == nil {
			return nil, false, true, nil
		}
		var ok bool
		smtpSettings.Username = uri.User.Username()
		smtpSettings.Password, ok = uri.User.Password()
		if !ok {
			return nil, false, true, nil
		}
		smtpSettings.Port = uri.Port()
		if smtpSettings.Port == "" {
			return nil, false, true, nil
		}
		smtpSettings.Host = strings.TrimSuffix(uri.Host, ":"+smtpSettings.Port)
		return smtpSettings, false, false, nil
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
				"stylesCSS":   func() template.CSS { return template.CSS(stylesCSS) },
				"baselineJS":  func() template.JS { return template.JS(baselineJS) },
				"isResetLink": func() bool { return r.Form.Has("token") },
			}
			tmpl, err := template.New("resetpassword.html").Funcs(funcMap).ParseFS(rootFS, "embed/resetpassword.html")
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			contentSecurityPolicy(w, "", false)
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
		if isAuthenticated() {
			response.Status = ErrAlreadyAuthenticated
			writeResponse(w, r, response)
			return
		}
		if r.Form.Has("token") {
			resetTokenHash, expired, err := hashAndValidateResetToken(r.Form.Get("token"))
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if expired {
				response.Status = ErrTokenExpired
				writeResponse(w, r, response)
				return
			}
			if resetTokenHash == nil {
				response.Status = ErrInvalidToken
				writeResponse(w, r, response)
				return
			}
			response.ResetToken = r.Form.Get("token")
		} else {
			_, isDisabled, isMisconfigured, err := getSmtpSettings()
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if isDisabled {
				response.Status = ErrMailerNotEnabled
				writeResponse(w, r, response)
				return
			}
			if isMisconfigured {
				response.Status = ErrMailerMisconfigured
				writeResponse(w, r, response)
				return
			}
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
			if !response.Status.Success() || response.Status == SentPaswordResetLinkSuccess {
				err := nbrew.setSession(w, r, "flash", &response)
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				var query string
				if response.ResetToken != "" {
					query = "?token=" + url.QueryEscape(response.ResetToken)
				}
				http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/resetpassword/"+query, http.StatusFound)
				return
			}
			var status string
			if response.Status == ErrMailSendingFailed {
				status = string(response.Status) + ": " + response.MailSendingError
			} else {
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
			http.Redirect(w, r, nbrew.Scheme+nbrew.AdminDomain+"/admin/login/", http.StatusFound)
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
			request.Email = r.Form.Get("email")
			request.ResetToken = r.Form.Get("resetToken")
			request.Password = r.Form.Get("password")
			request.ConfirmPassword = r.Form.Get("confirmPassword")
		default:
			unsupportedContentType(w, r)
			return
		}

		response := Response{
			Email:      request.Email,
			ResetToken: request.ResetToken,
			Errors:     make(map[string][]Error),
		}
		if isAuthenticated() {
			response.Status = ErrAlreadyAuthenticated
			writeResponse(w, r, response)
			return
		}
		var resetTokenHash []byte
		if response.ResetToken != "" {
			var err error
			var expired bool
			resetTokenHash, expired, err = hashAndValidateResetToken(response.ResetToken)
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if expired {
				response.Status = ErrTokenExpired
				writeResponse(w, r, response)
				return
			}
			if resetTokenHash == nil {
				response.Status = ErrInvalidToken
				writeResponse(w, r, response)
				return
			}
		}

		if resetTokenHash == nil {
			if request.Email == "" {
				response.Errors["email"] = append(response.Errors["email"], ErrRequired)
			} else {
				_, err := mail.ParseAddress(request.Email)
				if err != nil {
					response.Errors["email"] = append(response.Errors["email"], ErrInvalidEmail)
				}
			}
			if len(response.Errors["email"]) == 0 {
				exists, err := sq.FetchExistsContext(r.Context(), nbrew.DB, sq.CustomQuery{
					Dialect: nbrew.Dialect,
					Format:  "SELECT 1 FROM users WHERE email = {email}",
					Values: []any{
						sq.StringParam("email", request.Email),
					},
				})
				if err != nil {
					getLogger(r.Context()).Error(err.Error())
					internalServerError(w, r, err)
					return
				}
				if !exists {
					response.Errors["email"] = append(response.Errors["email"], ErrUserNotFound)
				}
			}
		} else {
			if request.Password == "" {
				response.Errors["password"] = append(response.Errors["password"], ErrRequired)
			} else {
				if utf8.RuneCountInString(request.Password) < 8 {
					response.Errors["password"] = append(response.Errors["password"], Error(fmt.Sprintf("%s - minimum 8 characters", ErrTooShort)))
				}
				if IsCommonPassword([]byte(request.Password)) {
					response.Errors["password"] = append(response.Errors["password"], ErrPasswordTooCommon)
				}
			}
			if len(response.Errors["password"]) == 0 {
				if request.ConfirmPassword == "" {
					response.Errors["confirmPassword"] = append(response.Errors["confirmPassword"], ErrRequired)
				} else {
					if request.Password != request.ConfirmPassword {
						response.Errors["confirmPassword"] = append(response.Errors["confirmPassword"], ErrPasswordNotMatch)
					}
				}
			}
		}
		if len(response.Errors) > 0 {
			response.Status = ErrValidationFailed
			writeResponse(w, r, response)
			return
		}

		if resetTokenHash == nil {
			smtpSettings, isDisabled, isMisconfigured, err := getSmtpSettings()
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			if isDisabled {
				response.Status = ErrMailerNotEnabled
				writeResponse(w, r, response)
				return
			}
			if isMisconfigured {
				response.Status = ErrMailerMisconfigured
				writeResponse(w, r, response)
				return
			}
			resetToken := make([]byte, 8+16)
			binary.BigEndian.PutUint64(resetToken[:8], uint64(time.Now().Unix()))
			_, err = rand.Read(resetToken[8:])
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			checksum := blake2b.Sum256(resetToken[8:])
			resetTokenHash = make([]byte, 8+blake2b.Size256)
			copy(resetTokenHash[:8], resetToken[:8])
			copy(resetTokenHash[8:], checksum[:])
			_, err = sq.ExecContext(r.Context(), nbrew.DB, sq.CustomQuery{
				Dialect: nbrew.Dialect,
				Format:  "UPDATE users SET reset_token_hash = {resetTokenHash} WHERE email = {email}",
				Values: []any{
					sq.BytesParam("resetTokenHash", resetTokenHash),
					sq.StringParam("email", response.Email),
				},
			})
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				internalServerError(w, r, err)
				return
			}
			auth := smtp.PlainAuth("", smtpSettings.Username, smtpSettings.Password, smtpSettings.Host)
			from := "Notebrew mailer"
			to := strings.ReplaceAll(strings.ReplaceAll(response.Email, "\r", ""), "\n", "")
			subject := "Reset password"
			link := nbrew.Scheme + nbrew.AdminDomain + "/admin/resetpassword/?token=" + html.EscapeString(strings.TrimLeft(hex.EncodeToString(resetToken), "0"))
			msg := "MIME-version: 1.0\r\n" +
				"Content-Type: text/html; charset=\"UTF-8\"\r\n" +
				"From: " + from + "\r\n" +
				"To: " + to + "\r\n" +
				"Subject: " + subject + "\r\n" +
				"\r\n" +
				`Your password reset link is <a href="` + link + `">` + link + `</a>.<br><br>` +
				`If you do not wish to reset your password, ignore this message. It will expire in 20 minutes.<br><br>`
			err = smtp.SendMail(smtpSettings.Host+":"+smtpSettings.Port, auth, from, []string{to}, []byte(msg))
			if err != nil {
				getLogger(r.Context()).Error(err.Error())
				response.MailSendingError = err.Error()
				response.Status = ErrMailSendingFailed
				writeResponse(w, r, response)
				return
			}
			response.Status = SentPaswordResetLinkSuccess
			writeResponse(w, r, response)
			return
		}

		passwordHash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		tx, err := nbrew.DB.Begin()
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		defer tx.Rollback()
		_, err = sq.ExecContext(r.Context(), tx, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "DELETE FROM authentication WHERE EXISTS (" +
				"SELECT 1" +
				" FROM users" +
				" WHERE users.user_id = authentication.user_id" +
				" AND users.reset_token_hash = {resetTokenHash}" +
				")",
			Values: []any{
				sq.BytesParam("resetTokenHash", resetTokenHash),
			},
		})
		if err != nil {
			getLogger(r.Context()).Error(err.Error())
			internalServerError(w, r, err)
			return
		}
		_, err = sq.ExecContext(r.Context(), tx, sq.CustomQuery{
			Dialect: nbrew.Dialect,
			Format: "UPDATE users" +
				" SET password_hash = {passwordHash}" +
				", reset_token_hash = NULL" +
				" WHERE reset_token_hash = {resetTokenHash}",
			Values: []any{
				sq.StringParam("passwordHash", string(passwordHash)),
				sq.BytesParam("resetTokenHash", resetTokenHash),
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
		response.Status = ResetPasswordSuccess
		writeResponse(w, r, response)
	default:
		methodNotAllowed(w, r)
	}
}
