package bevigil

import (
	"context"
	"encoding/json"
	"fmt"
	"slack-wails/lib/gologger"

	"github.com/qiwentaidi/clients"
)

const bevigilURL = "https://osint.bevigil.com/"

type BevigilHosts struct {
	Domain     string   `json:"domain"`
	Subdomains []string `json:"subdomains"`
}

// subdomains return is the complete subdomain
func FetchHosts(ctx context.Context, domain, apikey string) *BevigilHosts {
	header := map[string]string{
		"X-Access-Token": apikey,
	}
	searchURL := fmt.Sprintf("%sapi/%s/subdomains/", bevigilURL, domain)
	resp, err := clients.DoRequest("GET", searchURL, header, nil, 10, clients.NewRestyClient(nil, true))
	if err != nil {
		gologger.Debug(ctx, err)
		return nil
	}
	// 积分不足
	if resp.StatusCode() == 402 {
		gologger.Debug(ctx, "No Credits left. Purchase new credits")
		return nil
	}
	bh := BevigilHosts{}
	json.Unmarshal(resp.Body(), &bh)
	return &bh
}
