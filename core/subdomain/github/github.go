package github

import (
	"context"
	"fmt"
	"regexp"
	"slack-wails/lib/gologger"
	"strings"

	"github.com/qiwentaidi/clients"
)

// subdomains return is complete subdomain
func FetchHosts(ctx context.Context, domain, apikey string) []string {
	headers := map[string]string{
		"Accept":        "application/vnd.github.v3.text-match+json",
		"Authorization": "token " + apikey,
	}
	searchURL := fmt.Sprintf("https://api.github.com/search/code?per_page=100&q=%s&sort=created&order=asc", domain)
	resp, err := clients.DoRequest("GET", searchURL, headers, nil, 10, clients.NewRestyClient(nil, true))
	if err != nil {
		gologger.Debug(ctx, err)
	}
	r := domainRegexp(domain)
	return r.FindAllString(string(resp.Body()), -1)
}

func domainRegexp(domain string) *regexp.Regexp {
	rdomain := strings.ReplaceAll(domain, ".", "\\.")
	return regexp.MustCompile("(\\w[a-zA-Z0-9][a-zA-Z0-9-\\.]*)" + rdomain)
}
