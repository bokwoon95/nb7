package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/namecheap"
)

func init() {
	resp, err := http.Get("https://ipv4.icanhazip.com")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println(err)
		return
	}
	clientIP := strings.TrimSpace(string(body))
	_ = clientIP

	certmagic.DefaultACME.DNS01Solver = &certmagic.DNS01Solver{
		DNSProvider: &namecheap.Provider{},
	}
}
