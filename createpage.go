package nb7

import "net/http"

func (nbrew *Notebrew) createpage(w http.ResponseWriter, r *http.Request, username, sitePrefix string) {
	type Request struct {
		Name    string `json:"name,omitempty"`
		Content string `json:"content,omitempty"`
	}
	type Response struct {
		Status Error  `json:"status"`
		Name   string `json:"name,omitempty"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20 /* 2MB */)
	switch r.Method {
	case "":
	}
}
