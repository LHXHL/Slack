package isic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slack-wails/lib/clients"
	"slack-wails/lib/gologger"
	"slack-wails/lib/structs"
	"strings"
)

type GithubResponse struct {
	Incomplete_results bool    `json:"incomplete_results"`
	Items              []Items `json:"items"`
	Total_count        float64 `json:"total_count"`
}

type Items struct {
	Path         string         `json:"path"`
	Sha          string         `json:"sha"`
	Git_url      string         `json:"git_url"`
	Html_url     string         `json:"html_url"`
	Name         string         `json:"name"`
	Url          string         `json:"url"`
	Repository   interface{}    `json:"repository"`
	Score        float64        `json:"score"`
	Text_matches []Text_matches `json:"text_matches"`
}

type Text_matches struct {
	Object_url  string    `json:"object_url"`
	Object_type string    `json:"object_type"`
	Property    string    `json:"property"`
	Fragment    string    `json:"fragment"`
	Matches     []Matches `json:"matches"`
}

type Matches struct {
	Indices []float64 `json:"indices"`
	Text    string    `json:"text"`
}

func GithubApiQuery(ctx context.Context, dork, token string) *structs.ISICollectionResult {
	uri, _ := url.Parse("https://api.github.com/search/code")
	param := url.Values{}
	param.Set("q", dork)
	param.Set("per_page", "100")
	uri.RawQuery = param.Encode()
	headers := map[string]string{
		"accept":        "application/vnd.github.v3+json",
		"Authorization": fmt.Sprintf("token %s", token),
		"User-Agent":    "HelloGitHub",
	}
	response, err := clients.DoRequest("GET", uri.String(), headers, nil, 10, clients.NewRestyClient(nil, true))
	if err != nil {
		gologger.Debug(ctx, err)
		return &structs.ISICollectionResult{}
	}
	var resp GithubResponse
	json.Unmarshal(response.Body(), &resp)
	gr := structs.ISICollectionResult{
		Query:  dork,
		Total:  resp.Total_count,
		Source: "Github",
	}
	gr.Total = resp.Total_count
	for _, item := range resp.Items {
		gr.Items = append(gr.Items, item.Html_url)
	}
	gr.Link = strings.ReplaceAll(uri.String(), "https://api.github.com/search/code", "https://github.com/search") + "&s=indexed&type=Code&o=desc"
	return &gr
}
